package mujianconfig

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/mujianprovider"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	DefaultChatModel         = "claude-opus-5"
	CutoverChatModel         = "claude-sonnet-4-6"
	assignmentLedgerModel    = CutoverChatModel
	assignmentLedgerPrefix   = "_mujian_default_chat_model_auto:sonnet-4-6:"
	assignmentLedgerShards   = 64
	assignmentLedgerVersion  = 2
	assignmentGateKey        = "_mujian_default_chat_model_auto:gate"
	assignmentGateOpenPrefix = "open:"
	assignmentGateClosed     = "closed"
)

var defaultChatModels = []string{
	DefaultChatModel, CutoverChatModel, "deepseek-v4-flash", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna",
	"claude-fable-5-nc", "claude-sonnet-5", "claude-haiku-4-5", "grok-4.5",
}

type assignmentLedger struct {
	Version     int    `json:"version"`
	Model       string `json:"model"`
	OperationID string `json:"operation_id"`
	Shard       int    `json:"shard"`
	UserIDs     []int  `json:"user_ids"`
}

// DefaultChatModels returns the built-in chat allowlist without exposing the
// package-owned slice to callers.
func DefaultChatModels() []string {
	return append([]string(nil), defaultChatModels...)
}

// EnabledChatModels returns the configured chat allowlist. An empty
// MUJIAN_CHAT_MODELS value preserves the built-in list.
func EnabledChatModels() []string {
	value := strings.TrimSpace(os.Getenv("MUJIAN_CHAT_MODELS"))
	if value == "" {
		return DefaultChatModels()
	}
	parts := strings.Split(value, ",")
	models := make([]string, 0, len(parts))
	for _, part := range parts {
		if candidate := strings.TrimSpace(part); candidate != "" {
			models = append(models, candidate)
		}
	}
	return models
}

// ConfiguredDefaultChatModel is the shared startup and migration gate for
// MUJIAN_DEFAULT_CHAT_MODEL. Only registered chat models present in the active
// chat allowlist are accepted.
func ConfiguredDefaultChatModel() (string, error) {
	configured := strings.TrimSpace(os.Getenv("MUJIAN_DEFAULT_CHAT_MODEL"))
	if configured == "" {
		configured = DefaultChatModel
	}

	registeredChatModel := false
	for _, entry := range mujianprovider.Catalog() {
		if entry.ID == configured {
			registeredChatModel = entry.Kind == "chat"
			break
		}
	}
	if !registeredChatModel || !contains(EnabledChatModels(), configured) {
		return "", errors.New("MUJIAN_DEFAULT_CHAT_MODEL 必须是已登记且已启用的对话模型")
	}
	return configured, nil
}

// RequireConfiguredDefaultChatModel additionally pins a mutating rollout
// command to the expected side of the cutover.
func RequireConfiguredDefaultChatModel(expected string) error {
	configured, err := ConfiguredDefaultChatModel()
	if err != nil {
		return err
	}
	if configured != expected {
		return fmt.Errorf("MUJIAN_DEFAULT_CHAT_MODEL must be %q for this applied operation (got %q)", expected, configured)
	}
	return nil
}

// AutomaticDefaultChatModelUserIDs returns the users whose Sonnet 4.6 choice
// still belongs to the rollout rather than to an explicit preference update.
func AutomaticDefaultChatModelUserIDs(db *gorm.DB, operationID string) ([]int, error) {
	if err := validateAssignmentOperationID(operationID); err != nil {
		return nil, err
	}
	gate, err := readAssignmentGate(db)
	if err != nil {
		return nil, err
	}
	if _, err = requireOpenAssignmentGate(gate, operationID); err != nil {
		return nil, err
	}
	ledgers, err := loadAssignmentLedgers(db, allAssignmentShards(), false, operationID)
	if err != nil {
		return nil, err
	}
	return ledgerUserIDs(ledgers), nil
}

// AddAutomaticDefaultChatModelUsers records automatic assignments in the same
// transaction as their preference writes. The fixed shard set keeps the
// existing options table bounded and avoids a per-user option row.
func AddAutomaticDefaultChatModelUsers(db *gorm.DB, userIDs ...int) error {
	shards, err := assignmentShards(userIDs)
	if err != nil {
		return err
	}
	if len(shards) == 0 {
		return nil
	}
	operationID, err := requireAssignmentGateOpen(db)
	if err != nil {
		return err
	}
	ledgers, err := loadAssignmentLedgers(db, shards, true, operationID)
	if err != nil {
		return err
	}
	seenByShard := make(map[int]map[int]struct{}, len(ledgers))
	changed := make(map[int]assignmentLedger, len(ledgers))
	for shard, ledger := range ledgers {
		seen := make(map[int]struct{}, len(ledger.UserIDs))
		for _, userID := range ledger.UserIDs {
			seen[userID] = struct{}{}
		}
		seenByShard[shard] = seen
	}
	for _, userID := range userIDs {
		shard := assignmentShard(userID)
		if _, exists := seenByShard[shard][userID]; exists {
			continue
		}
		ledger := ledgers[shard]
		ledger.UserIDs = append(ledger.UserIDs, userID)
		ledgers[shard] = ledger
		changed[shard] = ledger
		seenByShard[shard][userID] = struct{}{}
	}
	return saveAssignmentLedgers(db, changed)
}

// OpenAutomaticDefaultChatModelAssignments starts (or restarts) a cutover
// assignment window. The migration calls this while holding its transaction,
// before changing preferences, so onboarding cannot race the cutover boundary.
func OpenAutomaticDefaultChatModelAssignments(db *gorm.DB, operationID string) error {
	if err := validateAssignmentOperationID(operationID); err != nil {
		return err
	}
	gate, err := lockAssignmentGate(db, "UPDATE")
	if err != nil {
		return err
	}
	if gate.Value != assignmentGateClosed {
		return errors.New("automatic default chat model assignments are already open")
	}
	if err = initializeAssignmentLedgers(db, operationID); err != nil {
		return err
	}
	return setAssignmentGate(db, assignmentGateOpenValue(operationID))
}

// RemoveAutomaticDefaultChatModelUser preserves an explicit user choice by
// removing that user from the automatic-assignment ledger atomically with the
// preference update.
func RemoveAutomaticDefaultChatModelUser(db *gorm.DB, userID int) error {
	shards, err := assignmentShards([]int{userID})
	if err != nil {
		return err
	}
	gate, err := lockAssignmentGate(db, "SHARE")
	if err != nil {
		return err
	}
	if gate.Value == assignmentGateClosed {
		return nil
	}
	operationID, err := requireOpenAssignmentGate(gate, "")
	if err != nil {
		return err
	}
	ledgers, err := loadAssignmentLedgers(db, shards, true, operationID)
	if err != nil {
		return err
	}
	shard := assignmentShard(userID)
	ledger := ledgers[shard]
	for index, candidate := range ledger.UserIDs {
		if candidate == userID {
			ledger.UserIDs = append(ledger.UserIDs[:index], ledger.UserIDs[index+1:]...)
			ledgers[shard] = ledger
			return saveAssignmentLedgers(db, ledgers)
		}
	}
	return nil
}

// ConsumeAutomaticDefaultChatModelUsers is the rollback preflight. It locks
// every shard, rejects concurrent ledger drift, and clears the exact frozen set
// in the caller's transaction. A rollback can therefore prove that it leaves
// no automatic Sonnet 4.6 assignment behind.
func ConsumeAutomaticDefaultChatModelUsers(db *gorm.DB, operationID string, expected []int) error {
	if err := validateAssignmentOperationID(operationID); err != nil {
		return err
	}
	gate, err := lockAssignmentGate(db, "UPDATE")
	if err != nil {
		return err
	}
	if _, err = requireOpenAssignmentGate(gate, operationID); err != nil {
		return err
	}
	ledgers, err := loadAssignmentLedgers(db, allAssignmentShards(), true, operationID)
	if err != nil {
		return err
	}
	actual := ledgerUserIDs(ledgers)
	want := append([]int(nil), expected...)
	sort.Ints(want)
	if !equalInts(actual, want) {
		return errors.New("automatic default chat model assignments changed; rerun rollback preflight")
	}
	changed := make(map[int]assignmentLedger)
	for shard, ledger := range ledgers {
		if len(ledger.UserIDs) == 0 {
			continue
		}
		ledger.UserIDs = nil
		changed[shard] = ledger
	}
	if err = saveAssignmentLedgers(db, changed); err != nil {
		return err
	}
	return setAssignmentGate(db, assignmentGateClosed)
}

func requireAssignmentGateOpen(db *gorm.DB) (string, error) {
	gate, err := lockAssignmentGate(db, "SHARE")
	if err != nil {
		return "", err
	}
	return requireOpenAssignmentGate(gate, "")
}

func requireOpenAssignmentGate(gate model.Option, expectedOperationID string) (string, error) {
	if gate.Value == assignmentGateClosed {
		return "", errors.New("automatic default chat model assignments are closed")
	}
	operationID := strings.TrimPrefix(gate.Value, assignmentGateOpenPrefix)
	if operationID == gate.Value || validateAssignmentOperationID(operationID) != nil {
		return "", errors.New("automatic default chat model assignment gate is invalid")
	}
	if expectedOperationID != "" && operationID != expectedOperationID {
		return "", errors.New("automatic default chat model assignment operation does not match the active cutover")
	}
	return operationID, nil
}

func lockAssignmentGate(db *gorm.DB, strength string) (model.Option, error) {
	gate := model.Option{Key: assignmentGateKey, Value: assignmentGateClosed}
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&gate).Error; err != nil {
		return model.Option{}, fmt.Errorf("initialize automatic default chat model assignment gate: %w", err)
	}
	gate = model.Option{}
	if err := db.Clauses(clause.Locking{Strength: strength}).First(&gate, "key = ?", assignmentGateKey).Error; err != nil {
		return model.Option{}, fmt.Errorf("lock automatic default chat model assignment gate: %w", err)
	}
	if gate.Value != assignmentGateClosed {
		if _, err := requireOpenAssignmentGate(gate, ""); err != nil {
			return model.Option{}, err
		}
	}
	return gate, nil
}

func readAssignmentGate(db *gorm.DB) (model.Option, error) {
	var gate model.Option
	err := db.First(&gate, "key = ?", assignmentGateKey).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.Option{Key: assignmentGateKey, Value: assignmentGateClosed}, nil
	}
	if err != nil {
		return model.Option{}, fmt.Errorf("load automatic default chat model assignment gate: %w", err)
	}
	if gate.Value != assignmentGateClosed {
		if _, err = requireOpenAssignmentGate(gate, ""); err != nil {
			return model.Option{}, err
		}
	}
	return gate, nil
}

func setAssignmentGate(db *gorm.DB, value string) error {
	result := db.Model(&model.Option{}).Where("key = ?", assignmentGateKey).Update("value", value)
	if result.Error != nil {
		return fmt.Errorf("set automatic default chat model assignment gate: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return errors.New("automatic default chat model assignment gate disappeared")
	}
	return nil
}

func initializeAssignmentLedgers(db *gorm.DB, operationID string) error {
	options := make([]model.Option, 0, assignmentLedgerShards)
	for _, shard := range allAssignmentShards() {
		value, err := encodeAssignmentLedger(newAssignmentLedger(operationID, shard), shard)
		if err != nil {
			return err
		}
		options = append(options, model.Option{Key: assignmentLedgerKey(shard), Value: value})
	}
	if err := db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(&options).Error; err != nil {
		return fmt.Errorf("initialize automatic default chat model ledger: %w", err)
	}
	_, err := loadAssignmentLedgers(db, allAssignmentShards(), true, operationID)
	return err
}

func loadAssignmentLedgers(db *gorm.DB, shards []int, lock bool, operationID string) (map[int]assignmentLedger, error) {
	keys := make([]string, 0, len(shards))
	for _, shard := range shards {
		keys = append(keys, assignmentLedgerKey(shard))
	}

	rows := make([]model.Option, 0, len(keys))
	query := db.Where("key IN ?", keys)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := query.Order("key ASC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load automatic default chat model ledger: %w", err)
	}
	if len(rows) != len(keys) {
		return nil, fmt.Errorf("automatic default chat model ledger is incomplete: found %d of %d shards", len(rows), len(keys))
	}
	ledgers := make(map[int]assignmentLedger, len(shards))
	for _, row := range rows {
		shard, err := parseAssignmentLedgerKey(row.Key)
		if err != nil {
			return nil, err
		}
		ledger, err := decodeAssignmentLedger(row.Value, shard, operationID)
		if err != nil {
			return nil, err
		}
		ledgers[shard] = ledger
	}
	return ledgers, nil
}

func saveAssignmentLedgers(db *gorm.DB, ledgers map[int]assignmentLedger) error {
	shards := make([]int, 0, len(ledgers))
	for shard := range ledgers {
		shards = append(shards, shard)
	}
	sort.Ints(shards)
	for _, shard := range shards {
		ledger := ledgers[shard]
		sort.Ints(ledger.UserIDs)
		value, err := encodeAssignmentLedger(ledger, shard)
		if err != nil {
			return err
		}
		result := db.Model(&model.Option{}).Where("key = ?", assignmentLedgerKey(shard)).Update("value", value)
		if result.Error != nil {
			return fmt.Errorf("save automatic default chat model ledger: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return errors.New("automatic default chat model ledger row disappeared")
		}
	}
	return nil
}

func decodeAssignmentLedger(value string, shard int, operationID string) (assignmentLedger, error) {
	var ledger assignmentLedger
	if err := common.UnmarshalJsonStr(value, &ledger); err != nil {
		return assignmentLedger{}, fmt.Errorf("decode automatic default chat model ledger shard %d: %w", shard, err)
	}
	if ledger.Version != assignmentLedgerVersion || ledger.Model != assignmentLedgerModel ||
		ledger.OperationID != operationID || ledger.Shard != shard {
		return assignmentLedger{}, fmt.Errorf("automatic default chat model ledger shard %d has an unsupported format", shard)
	}
	previous := 0
	for index, userID := range ledger.UserIDs {
		if userID <= 0 || assignmentShard(userID) != shard || (index > 0 && userID <= previous) {
			return assignmentLedger{}, fmt.Errorf("automatic default chat model ledger shard %d is invalid", shard)
		}
		previous = userID
	}
	return ledger, nil
}

func encodeAssignmentLedger(ledger assignmentLedger, shard int) (string, error) {
	if err := validateAssignmentOperationID(ledger.OperationID); err != nil {
		return "", err
	}
	ledger.Version = assignmentLedgerVersion
	ledger.Model = assignmentLedgerModel
	ledger.Shard = shard
	if ledger.UserIDs == nil {
		ledger.UserIDs = []int{}
	}
	data, err := common.Marshal(ledger)
	if err != nil {
		return "", fmt.Errorf("encode automatic default chat model ledger: %w", err)
	}
	return string(data), nil
}

func newAssignmentLedger(operationID string, shard int) assignmentLedger {
	return assignmentLedger{
		Version: assignmentLedgerVersion, Model: assignmentLedgerModel,
		OperationID: operationID, Shard: shard, UserIDs: []int{},
	}
}

func assignmentGateOpenValue(operationID string) string {
	return assignmentGateOpenPrefix + operationID
}

func validateAssignmentOperationID(operationID string) error {
	parsed, err := uuid.Parse(operationID)
	if err != nil || parsed.String() != operationID {
		return errors.New("automatic default chat model operation id must be a canonical UUID")
	}
	return nil
}

func assignmentShards(userIDs []int) ([]int, error) {
	seen := make(map[int]struct{})
	for _, userID := range userIDs {
		if userID <= 0 {
			return nil, fmt.Errorf("automatic default chat model user id must be positive: %d", userID)
		}
		seen[assignmentShard(userID)] = struct{}{}
	}
	shards := make([]int, 0, len(seen))
	for shard := range seen {
		shards = append(shards, shard)
	}
	sort.Ints(shards)
	return shards, nil
}

func allAssignmentShards() []int {
	shards := make([]int, assignmentLedgerShards)
	for shard := range shards {
		shards[shard] = shard
	}
	return shards
}

func assignmentShard(userID int) int {
	return userID % assignmentLedgerShards
}

func assignmentLedgerKey(shard int) string {
	return assignmentLedgerPrefix + fmt.Sprintf("%02d", shard)
}

func parseAssignmentLedgerKey(key string) (int, error) {
	value := strings.TrimPrefix(key, assignmentLedgerPrefix)
	if value == key {
		return 0, errors.New("automatic default chat model ledger key is invalid")
	}
	shard, err := strconv.Atoi(value)
	if err != nil || shard < 0 || shard >= assignmentLedgerShards || assignmentLedgerKey(shard) != key {
		return 0, errors.New("automatic default chat model ledger key is invalid")
	}
	return shard, nil
}

func ledgerUserIDs(ledgers map[int]assignmentLedger) []int {
	userIDs := make([]int, 0)
	for _, ledger := range ledgers {
		userIDs = append(userIDs, ledger.UserIDs...)
	}
	sort.Ints(userIDs)
	return userIDs
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func equalInts(left, right []int) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
