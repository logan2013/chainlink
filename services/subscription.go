package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/smartcontractkit/chainlink/logger"
	"github.com/smartcontractkit/chainlink/store"
	"github.com/smartcontractkit/chainlink/store/models"
	"github.com/smartcontractkit/chainlink/store/presenters"
	"github.com/smartcontractkit/chainlink/utils"
	"go.uber.org/multierr"
)

// Descriptive indices of a RunLog's Topic array
const (
	RunLogTopicSignature = iota
	RunLogTopicInternalID
	RunLogTopicJobID
	RunLogTopicAmount
)

// RunLogTopic is the signature for the RunRequest(...) event
// which Chainlink RunLog initiators watch for.
// See https://github.com/smartcontractkit/chainlink/blob/master/solidity/contracts/Oracle.sol
// If updating this, be sure to update the truffle suite's "expected event signature" test.
var RunLogTopic = common.HexToHash("0x3fab86a1207bdcfe3976d0d9df25f263d45ae8d381a60960559771a2b223974d")

// Unsubscriber is the interface for all subscriptions, allowing one to unsubscribe.
type Unsubscriber interface {
	Unsubscribe()
}

// JobSubscription listens to event logs being pushed from the Ethereum Node to a job.
type JobSubscription struct {
	Job           models.JobSpec
	unsubscribers []Unsubscriber
}

// StartJobSubscription is the constructor of JobSubscription that to starts
// listening to and keeps track of event logs corresponding to a job.
func StartJobSubscription(job models.JobSpec, head *models.IndexableBlockNumber, store *store.Store) (JobSubscription, error) {
	var merr error
	var initSubs []Unsubscriber
	for _, initr := range job.InitiatorsFor(models.InitiatorEthLog) {
		sub, err := StartEthLogSubscription(initr, job, head, store)
		merr = multierr.Append(merr, err)
		if err == nil {
			initSubs = append(initSubs, sub)
		}
	}

	for _, initr := range job.InitiatorsFor(models.InitiatorRunLog) {
		sub, err := StartRunLogSubscription(initr, job, head, store)
		merr = multierr.Append(merr, err)
		if err == nil {
			initSubs = append(initSubs, sub)
		}
	}

	if len(initSubs) == 0 {
		return JobSubscription{}, multierr.Append(merr, errors.New("Job must have a valid log initiator"))
	}

	js := JobSubscription{Job: job, unsubscribers: initSubs}
	return js, merr
}

// Unsubscribe stops the subscription and cleans up associated resources.
func (js JobSubscription) Unsubscribe() {
	for _, sub := range js.unsubscribers {
		sub.Unsubscribe()
	}
}

// NewInitiatorFilterQuery returns a new InitiatorSubscriber with initialized filter.
func NewInitiatorFilterQuery(
	initr models.Initiator,
	head *models.IndexableBlockNumber,
	topics [][]common.Hash,
) ethereum.FilterQuery {
	listenFromNumber := head.NextInt()
	q := utils.ToFilterQueryFor(listenFromNumber, []common.Address{initr.Address})
	q.Topics = topics
	return q
}

// InitiatorSubscription encapsulates all functionality needed to wrap an ethereum subscription
// for use with a Chainlink Initiator. Initiator specific functionality is delegated
// to the callback.
type InitiatorSubscription struct {
	*ManagedSubscription
	Job       models.JobSpec
	Initiator models.Initiator
	store     *store.Store
	callback  func(InitiatorSubscriptionLogEvent)
}

// NewInitiatorSubscription creates a new InitiatorSubscription that feeds received
// logs to the callback func parameter.
func NewInitiatorSubscription(
	initr models.Initiator,
	job models.JobSpec,
	store *store.Store,
	filter ethereum.FilterQuery,
	callback func(InitiatorSubscriptionLogEvent),
) (InitiatorSubscription, error) {
	if !initr.IsLogInitiated() {
		return InitiatorSubscription{}, errors.New("Can only create an initiator subscription for log initiators")
	}

	sub := InitiatorSubscription{
		Job:       job,
		Initiator: initr,
		store:     store,
		callback:  callback,
	}

	managedSub, err := NewManagedSubscription(store, filter, sub.dispatchLog)
	if err != nil {
		return sub, err
	}

	sub.ManagedSubscription = managedSub
	loggerLogListening(initr, filter.FromBlock)
	return sub, nil
}

func (sub InitiatorSubscription) dispatchLog(log types.Log) {
	sub.callback(InitiatorSubscriptionLogEvent{
		Job:       sub.Job,
		Initiator: sub.Initiator,
		Log:       log,
		store:     sub.store,
	})
}

// TopicFiltersForRunLog generates the two variations of RunLog IDs that could
// possibly be entered. There is the ID, hex encoded and the ID zero padded.
func TopicFiltersForRunLog(jobID string) [][]common.Hash {
	hexJobID := common.BytesToHash([]byte(jobID))
	jobIDZeroPadded := common.BytesToHash(common.RightPadBytes(hexutil.MustDecode("0x"+jobID), utils.EVMWordByteLen))
	// RunLogTopic AND (0xHEXJOBID OR 0xJOBID0padded)
	return [][]common.Hash{{RunLogTopic}, nil, {hexJobID, jobIDZeroPadded}}
}

// StartRunLogSubscription starts an InitiatorSubscription tailored for use with RunLogs.
func StartRunLogSubscription(initr models.Initiator, job models.JobSpec, head *models.IndexableBlockNumber, store *store.Store) (Unsubscriber, error) {
	filter := NewInitiatorFilterQuery(initr, head, TopicFiltersForRunLog(job.ID))
	return NewInitiatorSubscription(initr, job, store, filter, receiveRunLog)
}

// StartEthLogSubscription starts an InitiatorSubscription tailored for use with EthLogs.
func StartEthLogSubscription(initr models.Initiator, job models.JobSpec, head *models.IndexableBlockNumber, store *store.Store) (Unsubscriber, error) {
	filter := NewInitiatorFilterQuery(initr, head, nil)
	return NewInitiatorSubscription(initr, job, store, filter, receiveEthLog)
}

func loggerLogListening(initr models.Initiator, blockNumber *big.Int) {
	msg := fmt.Sprintf(
		"Listening for %v from block %v for address %v for job %v",
		initr.Type,
		presenters.FriendlyBigInt(blockNumber),
		presenters.LogListeningAddress(initr.Address),
		initr.JobID)
	logger.Infow(msg)
}

// Parse the log and run the job specific to this initiator log event.
func receiveRunLog(le InitiatorSubscriptionLogEvent) {
	if !le.ValidateRunLog() {
		return
	}

	le.ToDebug()
	data, err := le.RunLogJSON()
	if err != nil {
		logger.Errorw(err.Error(), le.ForLogger()...)
		return
	}

	runJob(le, data, le.Initiator)
}

// Parse the log and run the job specific to this initiator log event.
func receiveEthLog(le InitiatorSubscriptionLogEvent) {
	le.ToDebug()
	data, err := le.EthLogJSON()
	if err != nil {
		logger.Errorw(err.Error(), le.ForLogger()...)
		return
	}

	runJob(le, data, le.Initiator)
}

func runJob(le InitiatorSubscriptionLogEvent, data models.JSON, initr models.Initiator) {
	payment, err := le.ContractPayment()
	if err != nil {
		logger.Errorw(err.Error(), le.ForLogger()...)
		return
	}
	input := models.RunResult{
		Data:   data,
		Amount: payment,
	}
	if _, err := BeginRunAtBlock(le.Job, initr, input, le.store, le.ToIndexableBlockNumber()); err != nil {
		logger.Errorw(err.Error(), le.ForLogger()...)
	}
}

// ManagedSubscription encapsulates the connecting, backfilling, and clean up of an
// ethereum node subscription.
type ManagedSubscription struct {
	store           *store.Store
	logs            chan types.Log
	errors          chan error
	ethSubscription models.EthSubscription
	callback        func(types.Log)
}

// NewManagedSubscription subscribes to the ethereum node with the passed filter
// and delegates incoming logs to callback.
func NewManagedSubscription(
	store *store.Store,
	filter ethereum.FilterQuery,
	callback func(types.Log),
) (*ManagedSubscription, error) {
	logs := make(chan types.Log)
	es, err := store.TxManager.SubscribeToLogs(logs, filter)
	if err != nil {
		return nil, err
	}

	sub := &ManagedSubscription{
		store:           store,
		callback:        callback,
		logs:            logs,
		ethSubscription: es,
		errors:          make(chan error),
	}
	go sub.listenToSubscriptionErrors()
	go sub.listenToLogs(filter)
	return sub, nil
}

// Unsubscribe closes channels and cleans up resources.
func (sub ManagedSubscription) Unsubscribe() {
	if sub.ethSubscription != nil {
		sub.ethSubscription.Unsubscribe()
	}
	close(sub.logs)
	close(sub.errors)
}

func (sub ManagedSubscription) listenToSubscriptionErrors() {
	for err := range sub.errors {
		logger.Errorw(fmt.Sprintf("Error in log subscription: %s", err.Error()), "err", err)
	}
}

func (sub ManagedSubscription) listenToLogs(q ethereum.FilterQuery) {
	backfilledSet := sub.backfillLogs(q)
	for log := range sub.logs {
		if _, present := backfilledSet[log.BlockHash.String()]; !present {
			sub.callback(log)
		}
	}
}

func (sub ManagedSubscription) backfillLogs(q ethereum.FilterQuery) map[string]bool {
	backfilledSet := map[string]bool{}
	if q.FromBlock.Cmp(big.NewInt(0)) <= 0 {
		return backfilledSet
	}

	logs, err := sub.store.TxManager.GetLogs(q)
	if err != nil {
		logger.Errorw("Unable to backfill logs", "err", err)
		return backfilledSet
	}

	for _, log := range logs {
		backfilledSet[log.BlockHash.String()] = true
		sub.callback(log)
	}
	return backfilledSet
}

// InitiatorSubscriptionLogEvent encapsulates all information as a result of a received log from an
// InitiatorSubscription.
type InitiatorSubscriptionLogEvent struct {
	Log       types.Log
	Job       models.JobSpec
	Initiator models.Initiator
	store     *store.Store
}

// ForLogger formats the InitiatorSubscriptionLogEvent for easy common formatting in logs (trace statements, not ethereum events).
func (le InitiatorSubscriptionLogEvent) ForLogger(kvs ...interface{}) []interface{} {
	output := []interface{}{
		"job", le.Job,
		"log", le.Log,
		"initiator", le.Initiator,
	}

	return append(kvs, output...)
}

// ToDebug prints this event via logger.Debug.
func (le InitiatorSubscriptionLogEvent) ToDebug() {
	friendlyAddress := presenters.LogListeningAddress(le.Initiator.Address)
	msg := fmt.Sprintf("Received log from block #%v for address %v for job %v", le.Log.BlockNumber, friendlyAddress, le.Job.ID)
	logger.Debugw(msg, le.ForLogger()...)
}

// ToIndexableBlockNumber returns an IndexableBlockNumber for the given InitiatorSubscriptionLogEvent Block
func (le InitiatorSubscriptionLogEvent) ToIndexableBlockNumber() *models.IndexableBlockNumber {
	num := new(big.Int)
	num.SetUint64(le.Log.BlockNumber)
	return models.NewIndexableBlockNumber(num, le.Log.BlockHash)
}

// ValidateRunLog returns whether or not the contained log is a RunLog,
// a specific Chainlink event trigger from smart contracts.
func (le InitiatorSubscriptionLogEvent) ValidateRunLog() bool {
	el := le.Log
	if !isRunLog(el) {
		logger.Errorw("Skipping; Unable to retrieve runlog parameters from log", le.ForLogger()...)
		return false
	}

	jid, err := jobIDFromHexEncodedTopic(el)
	if err != nil {
		logger.Errorw("Failed to retrieve Job ID from log", le.ForLogger("err", err.Error())...)
		return false
	} else if jid != le.Job.ID && jobIDFromImproperEncodedTopic(el) != le.Job.ID {
		logger.Errorw(fmt.Sprintf("Run Log didn't have matching job ID: %v != %v", jid, le.Job.ID), le.ForLogger()...)
		return false
	}
	return true
}

// RunLogJSON extracts data from the log's topics and data specific to the format defined
// by RunLogs.
func (le InitiatorSubscriptionLogEvent) RunLogJSON() (models.JSON, error) {
	el := le.Log
	js, err := decodeABIToJSON(el.Data)
	if err != nil {
		return js, err
	}

	fullfillmentJSON, err := fulfillmentToJSON(le)
	if err != nil {
		return js, err
	}
	return js.Merge(fullfillmentJSON)
}

func fulfillmentToJSON(le InitiatorSubscriptionLogEvent) (models.JSON, error) {
	el := le.Log
	var js models.JSON
	js, err := js.Add("address", el.Address.String())
	if err != nil {
		return js, err
	}

	js, err = js.Add("dataPrefix", el.Topics[RunLogTopicInternalID].String())
	if err != nil {
		return js, err
	}

	return js.Add("functionSelector", OracleFulfillmentFunctionID)
}

// EthLogJSON reformats the log as JSON.
func (le InitiatorSubscriptionLogEvent) EthLogJSON() (models.JSON, error) {
	el := le.Log
	var out models.JSON
	b, err := json.Marshal(el)
	if err != nil {
		return out, err
	}
	return out, json.Unmarshal(b, &out)
}

// ContractPayment returns the amount attached to a contract to pay the Oracle upon fulfillment.
func (le InitiatorSubscriptionLogEvent) ContractPayment() (*big.Int, error) {
	if !isRunLog(le.Log) {
		return nil, nil
	}
	encodedAmount := le.Log.Topics[RunLogTopicAmount].Hex()
	payment, ok := new(big.Int).SetString(encodedAmount, 0)
	if !ok {
		return payment, fmt.Errorf("unable to decoded amount from RunLog: %s", encodedAmount)
	}
	return payment, nil
}

func decodeABIToJSON(data hexutil.Bytes) (models.JSON, error) {
	versionSize := 32
	varLocationSize := 32
	varLengthSize := 32
	prefix := versionSize + varLocationSize + varLengthSize
	hex := []byte(string([]byte(data)[prefix:]))
	return models.ParseCBOR(hex)
}

func isRunLog(log types.Log) bool {
	return len(log.Topics) == 4 && log.Topics[0] == RunLogTopic
}

func jobIDFromHexEncodedTopic(log types.Log) (string, error) {
	return utils.HexToString(log.Topics[RunLogTopicJobID].Hex())
}

func jobIDFromImproperEncodedTopic(log types.Log) string {
	return log.Topics[RunLogTopicJobID].String()[2:34]
}
