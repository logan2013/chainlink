// Package presenters allow for the specification and result
// of a Job, its associated TaskSpecs, and every JobRun and TaskRun
// to be returned in a user friendly human readable format.
package presenters

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/smartcontractkit/chainlink/logger"
	"github.com/smartcontractkit/chainlink/store"
	"github.com/smartcontractkit/chainlink/store/assets"
	"github.com/smartcontractkit/chainlink/store/models"
	"github.com/smartcontractkit/chainlink/utils"
	"github.com/tidwall/gjson"
)

// LogListeningAddress returns the LogListeningAddress
func LogListeningAddress(address common.Address) string {
	if address == utils.ZeroAddress {
		return "[all]"
	}
	return address.String()
}

// ShowEthBalance returns the current Eth Balance for current Account
func ShowEthBalance(store *store.Store) (string, error) {
	if !store.KeyStore.HasAccounts() {
		logger.Panic("KeyStore must have an account in order to show balance")
	}
	account, err := store.KeyStore.GetAccount()
	if err != nil {
		return "", err
	}
	address := account.Address
	balance, err := store.TxManager.GetEthBalance(address)
	if err != nil {
		return "", err
	}
	result := fmt.Sprintf("ETH Balance for %v: %v", address.Hex(), balance)
	if balance.IsZero() {
		return result, errors.New("0 Balance. Chainlink node not fully functional, please deposit ETH into your address: " + address.Hex())
	}
	return result, nil
}

// ShowLinkBalance returns the current Link Balance for current Account
func ShowLinkBalance(store *store.Store) (string, error) {
	if !store.KeyStore.HasAccounts() {
		logger.Panic("KeyStore must have an account in order to show balance")
	}
	account, err := store.KeyStore.GetAccount()
	if err != nil {
		return "", err
	}

	address := account.Address
	linkContractAddress := common.HexToAddress(store.Config.LinkContractAddress)
	linkBalance, err := store.TxManager.GetLinkBalance(address, linkContractAddress)
	if err != nil {
		return "", err
	}

	result := fmt.Sprintf("Link Balance for %v: %v", address.Hex(), linkBalance.String())
	return result, nil
}

// BridgeType holds a bridge.
type BridgeType struct {
	models.BridgeType
}

// MarshalJSON returns the JSON data of the Bridge.
func (bt BridgeType) MarshalJSON() ([]byte, error) {
	type Alias BridgeType
	return json.Marshal(&struct {
		Alias
	}{
		Alias(bt),
	})
}

// AccountBalance holds the hex representation of the address plus it's ETH & LINK balances
type AccountBalance struct {
	Address     string       `json:"address"`
	EthBalance  *assets.Eth  `json:"eth_balance"`
	LinkBalance *assets.Link `json:"link_balance"`
}

// GetID returns the ID of this structure for jsonapi serialization.
func (a AccountBalance) GetID() string {
	return a.Address
}

// SetID is used to set the ID of this structure when deserializing from jsonapi documents.
func (a *AccountBalance) SetID(value string) error {
	a.Address = value
	return nil
}

// ConfigWhitelist TODO...
type ConfigWhitelist struct {
	LogLevel               store.LogLevel  `json:"logLevel"`
	RootDir                string          `json:"root"`
	Port                   string          `json:"chainlinkPort"`
	GuiPort                string          `json:"guiPort"`
	BasicAuthUsername      string          `json:"username"`
	EthereumURL            string          `json:"ethUrl"`
	ChainID                uint64          `json:"ethChainId"`
	ClientNodeURL          string          `json:"clientNodeUrl"`
	TxMinConfirmations     uint64          `json:"txMinConfirmations"`
	TaskMinConfirmations   uint64          `json:"taskMinConfirmations"`
	EthGasBumpThreshold    uint64          `json:"ethGasBumpThreshold"`
	EthGasBumpWei          *big.Int        `json:"ethGasBumpWei"`
	EthGasPriceDefault     *big.Int        `json:"ethGasPriceDefault"`
	LinkContractAddress    string          `json:"linkContractAddress"`
	MinimumContractPayment *big.Int        `json:"minimumContractPayment"`
	OracleContractAddress  *common.Address `json:"oracleContractAddress"`
	DatabasePollInterval   store.Duration  `json:"databasePollInterval"`
}

// GetID generates a new ID for jsonapi serialization.
func (c ConfigWhitelist) GetID() string {
	return utils.NewBytes32ID()
}

// SetID is used to conform to the UnmarshallIdentifier interface for
// deserializing from jsonapi documents.
func (c *ConfigWhitelist) SetID(value string) error {
	return nil
}

// NewConfigWhitelist TODO...
func NewConfigWhitelist(config store.Config) ConfigWhitelist {
	return ConfigWhitelist{
		LogLevel:             config.LogLevel,
		RootDir:              config.RootDir,
		Port:                 config.Port,
		GuiPort:              config.GuiPort,
		BasicAuthUsername:    config.BasicAuthUsername,
		EthereumURL:          config.EthereumURL,
		ChainID:              config.ChainID,
		ClientNodeURL:        config.ClientNodeURL,
		TxMinConfirmations:   config.TxMinConfirmations,
		TaskMinConfirmations: config.TaskMinConfirmations,
		EthGasBumpThreshold:  config.EthGasBumpThreshold,
		EthGasBumpWei:        &config.EthGasBumpWei,
		EthGasPriceDefault:   &config.EthGasPriceDefault,
		// LinkContractAddress:  config.LinkContractAddress,
		MinimumContractPayment: &config.MinimumContractPayment,
		OracleContractAddress:  config.OracleContractAddress,
		DatabasePollInterval:   config.DatabasePollInterval,
	}
}

// JobSpec holds the JobSpec definition and each run associated with that Job.
type JobSpec struct {
	models.JobSpec
	Runs []models.JobRun `json:"runs,omitempty"`
}

// MarshalJSON returns the JSON data of the Job and its Initiators.
func (job JobSpec) MarshalJSON() ([]byte, error) {
	type Alias JobSpec
	pis := make([]Initiator, len(job.Initiators))
	for i, modelInitr := range job.Initiators {
		pis[i] = Initiator{modelInitr}
	}
	return json.Marshal(&struct {
		Initiators []Initiator `json:"initiators"`
		Alias
	}{
		pis,
		Alias(job),
	})
}

// FriendlyCreatedAt returns a human-readable string of the Job's
// CreatedAt field.
func (job JobSpec) FriendlyCreatedAt() string {
	return job.CreatedAt.HumanString()
}

// FriendlyStartAt returns a human-readable string of the Job's
// StartAt field.
func (job JobSpec) FriendlyStartAt() string {
	if job.StartAt.Valid {
		return utils.ISO8601UTC(job.StartAt.Time)
	}
	return ""
}

// FriendlyEndAt returns a human-readable string of the Job's
// EndAt field.
func (job JobSpec) FriendlyEndAt() string {
	if job.EndAt.Valid {
		return utils.ISO8601UTC(job.EndAt.Time)
	}
	return ""
}

// FriendlyInitiators returns the list of Initiator types as
// a comma separated string.
func (job JobSpec) FriendlyInitiators() string {
	var initrs []string
	for _, i := range job.Initiators {
		initrs = append(initrs, i.Type)
	}
	return strings.Join(initrs, "\n")
}

// FriendlyTasks returns the list of Task types as a comma
// separated string.
func (job JobSpec) FriendlyTasks() string {
	var tasks []string
	for _, t := range job.Tasks {
		tasks = append(tasks, t.Type)
	}

	return strings.Join(tasks, "\n")
}

// Initiator holds the Job definition's Initiator.
type Initiator struct {
	models.Initiator
}

// MarshalJSON returns the JSON data of the Initiator based
// on its Initiator Type.
func (i Initiator) MarshalJSON() ([]byte, error) {
	switch i.Type {
	case models.InitiatorWeb:
		return json.Marshal(&struct {
			Type string `json:"type"`
		}{
			models.InitiatorWeb,
		})
	case models.InitiatorCron:
		return json.Marshal(&struct {
			Type     string      `json:"type"`
			Schedule models.Cron `json:"schedule"`
		}{
			models.InitiatorCron,
			i.Schedule,
		})
	case models.InitiatorRunAt:
		return json.Marshal(&struct {
			Type string      `json:"type"`
			Time models.Time `json:"time"`
			Ran  bool        `json:"ran"`
		}{
			models.InitiatorRunAt,
			i.Time,
			i.Ran,
		})
	case models.InitiatorEthLog:
		return json.Marshal(&struct {
			Type    string         `json:"type"`
			Address common.Address `json:"address"`
		}{
			models.InitiatorEthLog,
			i.Address,
		})
	case models.InitiatorRunLog:
		return json.Marshal(&struct {
			Type    string         `json:"type"`
			Address common.Address `json:"address"`
		}{
			models.InitiatorRunLog,
			i.Address,
		})
	case models.InitiatorSpecAndRun:
		return json.Marshal(&struct {
			Type string `json:"type"`
		}{
			models.InitiatorSpecAndRun,
		})
	default:
		return nil, fmt.Errorf("Cannot marshal unsupported initiator type %v", i.Type)
	}
}

// FriendlyRunAt returns a human-readable string for Cron Initiator types.
func (i Initiator) FriendlyRunAt() string {
	if i.Type == models.InitiatorRunAt {
		return i.Time.HumanString()
	}
	return ""
}

var emptyAddress = common.Address{}.String()

// FriendlyAddress returns the Ethereum address if present, and a blank
// string if not.
func (i Initiator) FriendlyAddress() string {
	if i.IsLogInitiated() {
		return LogListeningAddress(i.Address)
	}
	return ""
}

// TaskSpec holds a task specified in the Job definition.
type TaskSpec struct {
	models.TaskSpec
}

// FriendlyParams returns a map of the TaskSpec's parameters.
func (t TaskSpec) FriendlyParams() (string, string) {
	keys := []string{}
	values := []string{}
	t.Params.ForEach(func(key, value gjson.Result) bool {
		if key.String() != "type" {
			keys = append(keys, key.String())
			values = append(values, value.String())
		}
		return true
	})
	return strings.Join(keys, "\n"), strings.Join(values, "\n")
}

// FriendlyBigInt returns a string printing the integer in both
// decimal and hexidecimal formats.
func FriendlyBigInt(n *big.Int) string {
	return fmt.Sprintf("#%[1]v (0x%[1]x)", n)
}
