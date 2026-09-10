package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/mujianconfig"
	"github.com/QuantumNous/new-api/service/mujianprovider"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	defaultChatModelCommand         = "default-chat-model"
	rollbackDefaultChatModelCommand = "rollback-default-chat-model"
	legacyDefaultChatModel          = mujianconfig.DefaultChatModel
	newDefaultChatModel             = mujianconfig.CutoverChatModel
	defaultModelReportKind          = "mujian_user_preference"
	defaultModelReportVersion       = 4
	defaultModelCommitOptionPrefix  = "_mujian_default_model_commit:"
	defaultModelActiveCutoverKey    = "_mujian_default_model_active_cutover"
	defaultModelCommittedPrefix     = "committed:"
	defaultModelRolledBackPrefix    = "rolled_back:"
)

type defaultModelChange struct {
	UserID int
	Group  string
	From   string
	To     string
}

type defaultModelPlanEntry struct {
	Change        defaultModelChange
	Status        string
	Ready         bool
	LedgerTracked bool
	Error         string
}

type defaultModelPlan struct {
	Command            string
	Database           string
	DeploymentID       string
	OperationID        string
	SourceOperationID  string
	SourceCommitDigest string
	DryRun             bool
	SuccessStatus      string
	AutomaticUserIDs   []int
	Entries            []defaultModelPlanEntry
}

type defaultModelSnapshot struct {
	OperationID  string
	CommitDigest string
}

func (e *migrationEngine) planDefaultChatModel(ctx context.Context, apply bool) (defaultModelPlan, error) {
	plan := defaultModelPlan{
		Command: defaultChatModelCommand, Database: e.database, DeploymentID: e.deploymentID,
		OperationID: uuid.NewString(), DryRun: !apply, SuccessStatus: "updated",
		Entries: []defaultModelPlanEntry{},
	}
	var preferences []model.MujianUserPreference
	if err := e.db.WithContext(ctx).
		Select("user_id", "default_chat_model").
		Where("default_chat_model = ?", legacyDefaultChatModel).
		Order("user_id ASC").Find(&preferences).Error; err != nil {
		return plan, fmt.Errorf("list legacy default chat model preferences: %w", err)
	}
	userIDs := make([]int, 0, len(preferences))
	for _, preference := range preferences {
		userIDs = append(userIDs, preference.UserID)
	}
	groups, err := loadDefaultModelUserGroups(e.db.WithContext(ctx), userIDs)
	if err != nil {
		return plan, err
	}
	routableByGroup := make(map[string]bool)
	status := "would_update"
	if apply {
		status = "will_update"
	}
	for _, preference := range preferences {
		group, exists := groups[preference.UserID]
		entry := defaultModelPlanEntry{Change: defaultModelChange{
			UserID: preference.UserID, Group: group,
			From: legacyDefaultChatModel, To: newDefaultChatModel,
		}}
		if !exists {
			entry.Status = "skipped_missing"
			plan.Entries = append(plan.Entries, entry)
			continue
		}
		routable, cached := routableByGroup[group]
		if !cached {
			var routeErr error
			routable, routeErr = defaultChatModelRoutable(e.db.WithContext(ctx), group)
			if routeErr != nil {
				return plan, routeErr
			}
			routableByGroup[group] = routable
		}
		if !routable {
			entry.Status = "skipped_unavailable"
			entry.Error = fmt.Sprintf("%s is not routable for user group %q", newDefaultChatModel, group)
			plan.Entries = append(plan.Entries, entry)
			continue
		}
		entry.Status = status
		entry.Ready = true
		plan.Entries = append(plan.Entries, entry)
	}
	return plan, nil
}

func (e *migrationEngine) planDefaultChatModelRollback(ctx context.Context, snapshotPath string, apply bool) (defaultModelPlan, error) {
	plan := defaultModelPlan{
		Command: rollbackDefaultChatModelCommand, Database: e.database, DeploymentID: e.deploymentID,
		OperationID: uuid.NewString(), DryRun: !apply, SuccessStatus: "restored",
		Entries: []defaultModelPlanEntry{},
	}
	snapshot, err := readDefaultModelSnapshot(snapshotPath, e.database, e.deploymentID)
	if err != nil {
		return plan, err
	}
	if err = requireDefaultModelCommitMarker(ctx, e.db, snapshot); err != nil {
		return plan, err
	}
	if err = requireDefaultModelActiveCutover(ctx, e.db, snapshot); err != nil {
		return plan, err
	}
	plan.SourceOperationID = snapshot.OperationID
	plan.SourceCommitDigest = snapshot.CommitDigest
	automaticUserIDs, err := mujianconfig.AutomaticDefaultChatModelUserIDs(e.db.WithContext(ctx), snapshot.OperationID)
	if err != nil {
		return plan, err
	}
	plan.AutomaticUserIDs = automaticUserIDs
	preferences, err := e.loadDefaultModelPreferences(ctx, automaticUserIDs)
	if err != nil {
		return plan, err
	}
	readyStatus := "would_restore"
	if apply {
		readyStatus = "will_restore"
	}
	for _, userID := range automaticUserIDs {
		entry := defaultModelPlanEntry{Change: defaultModelChange{
			UserID: userID,
			From:   newDefaultChatModel,
			To:     legacyDefaultChatModel,
		}, LedgerTracked: true}
		currentModel, exists := preferences[userID]
		switch {
		case !exists:
			entry.Status = "skipped_missing"
		case currentModel != newDefaultChatModel:
			entry.Status = "skipped_changed"
		default:
			entry.Status = readyStatus
			entry.Ready = true
		}
		plan.Entries = append(plan.Entries, entry)
	}
	return plan, nil
}

func (e *migrationEngine) executeDefaultModelPlan(
	ctx context.Context,
	plan defaultModelPlan,
	persistSnapshot func(migrationReport) error,
) (migrationReport, error) {
	if plan.DryRun {
		return plan.report(), errors.New("refusing to execute a dry-run default model plan")
	}
	if persistSnapshot == nil {
		return plan.report(), errors.New("an applied default model plan requires a durable snapshot writer")
	}
	if err := validateDefaultModelPlanIdentity(plan); err != nil {
		return plan.report(), err
	}
	var snapshot migrationReport
	snapshotPersisted := false
	err := e.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		readyIndexes, err := lockDefaultModelPlan(tx, &plan)
		if err != nil {
			return err
		}
		if plan.Command == defaultChatModelCommand {
			if err = requireNoDefaultModelActiveCutover(tx); err != nil {
				return err
			}
			if err = mujianconfig.OpenAutomaticDefaultChatModelAssignments(tx, plan.OperationID); err != nil {
				return err
			}
		} else {
			if err := lockDefaultModelSourceCommitMarker(tx, plan); err != nil {
				return err
			}
			if err := lockDefaultModelActiveCutover(tx, plan); err != nil {
				return err
			}
			if err = mujianconfig.ConsumeAutomaticDefaultChatModelUsers(tx, plan.SourceOperationID, plan.AutomaticUserIDs); err != nil {
				return err
			}
		}
		snapshot = plan.report()
		if err = sealDefaultModelReport(&snapshot); err != nil {
			return err
		}
		if err = persistSnapshot(snapshot); err != nil {
			return fmt.Errorf("persist default chat model snapshot: %w", err)
		}
		snapshotPersisted = true
		updatedUserIDs := make([]int, 0, len(readyIndexes))
		for _, index := range readyIndexes {
			change := plan.Entries[index].Change
			result := tx.Model(&model.MujianUserPreference{}).
				Where("user_id = ? AND default_chat_model = ?", change.UserID, change.From).
				Update("default_chat_model", change.To)
			if result.Error != nil {
				return fmt.Errorf("update user %d default chat model: %w", change.UserID, result.Error)
			}
			if result.RowsAffected != 1 {
				return fmt.Errorf("update user %d default chat model affected %d rows", change.UserID, result.RowsAffected)
			}
			updatedUserIDs = append(updatedUserIDs, change.UserID)
		}
		if plan.Command == defaultChatModelCommand {
			if err = mujianconfig.AddAutomaticDefaultChatModelUsers(tx, updatedUserIDs...); err != nil {
				return err
			}
		}
		if err = createDefaultModelCommitMarker(tx, snapshot); err != nil {
			return err
		}
		if plan.Command == defaultChatModelCommand {
			if err = createDefaultModelActiveCutover(tx, snapshot); err != nil {
				return err
			}
		} else {
			if err = consumeDefaultModelSourceCommitMarker(tx, plan, snapshot); err != nil {
				return err
			}
			if err = consumeDefaultModelActiveCutover(tx, plan); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		// A commit error can be ambiguous: the database may have committed even
		// though the client did not receive confirmation. Preserve the sealed
		// durable report so its transaction marker remains the source of truth.
		if snapshotPersisted {
			return snapshot, err
		}
		for index := range plan.Entries {
			if plan.Entries[index].Ready {
				plan.Entries[index].Status = "error"
				plan.Entries[index].Error = "transaction rolled back"
			}
		}
		return plan.report(), err
	}
	return snapshot, nil
}

func (e *migrationEngine) runDefaultModelPlan(ctx context.Context, reportPath string, plan defaultModelPlan) (migrationReport, error) {
	report := plan.report()
	if plan.DryRun {
		return report, nil
	}
	return e.executeDefaultModelPlan(ctx, plan, func(snapshot migrationReport) error {
		return writeMigrationReport(reportPath, snapshot)
	})
}

func lockDefaultModelPlan(tx *gorm.DB, plan *defaultModelPlan) ([]int, error) {
	const batchSize = 500

	readyIndexes := make([]int, 0, len(plan.Entries))
	userIDs := make([]int, 0, len(plan.Entries))
	for index, entry := range plan.Entries {
		if entry.Ready || entry.LedgerTracked {
			readyIndexes = append(readyIndexes, index)
			userIDs = append(userIDs, entry.Change.UserID)
		}
	}
	groups, err := loadDefaultModelUserGroups(tx.Clauses(clause.Locking{Strength: "UPDATE"}), userIDs)
	if err != nil {
		return nil, err
	}
	preferences := make(map[int]string, len(userIDs))
	for start := 0; start < len(userIDs); start += batchSize {
		end := start + batchSize
		if end > len(userIDs) {
			end = len(userIDs)
		}
		var batch []model.MujianUserPreference
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("user_id", "default_chat_model").
			Where("user_id IN ?", userIDs[start:end]).Find(&batch).Error; err != nil {
			return nil, fmt.Errorf("lock default chat model preferences: %w", err)
		}
		for _, preference := range batch {
			preferences[preference.UserID] = preference.DefaultChatModel
		}
	}

	lockedIndexes := readyIndexes[:0]
	routableByGroup := make(map[string]bool)
	for _, index := range readyIndexes {
		entry := &plan.Entries[index]
		currentModel, exists := preferences[entry.Change.UserID]
		switch {
		case !exists:
			entry.Status = "skipped_missing"
			entry.Ready = false
		case currentModel != entry.Change.From:
			entry.Status = "skipped_changed"
			entry.Ready = false
		case plan.Command == defaultChatModelCommand && groups[entry.Change.UserID] != entry.Change.Group:
			entry.Status = "skipped_changed"
			entry.Error = "user group changed after planning"
			entry.Ready = false
		default:
			if plan.Command == defaultChatModelCommand {
				group := entry.Change.Group
				routable, cached := routableByGroup[group]
				if !cached {
					var routeErr error
					routable, routeErr = defaultChatModelRoutable(tx, group)
					if routeErr != nil {
						return nil, routeErr
					}
					routableByGroup[group] = routable
				}
				if !routable {
					entry.Status = "skipped_unavailable"
					entry.Error = fmt.Sprintf("%s is no longer routable for user group %q", newDefaultChatModel, group)
					entry.Ready = false
					continue
				}
			}
			entry.Status = plan.SuccessStatus
			entry.Ready = true
			lockedIndexes = append(lockedIndexes, index)
		}
	}
	return lockedIndexes, nil
}

func (e *migrationEngine) loadDefaultModelPreferences(ctx context.Context, userIDs []int) (map[int]string, error) {
	const batchSize = 500

	preferences := make(map[int]string, len(userIDs))
	for start := 0; start < len(userIDs); start += batchSize {
		end := start + batchSize
		if end > len(userIDs) {
			end = len(userIDs)
		}
		var batch []model.MujianUserPreference
		if err := e.db.WithContext(ctx).
			Select("user_id", "default_chat_model").
			Where("user_id IN ?", userIDs[start:end]).Find(&batch).Error; err != nil {
			return nil, fmt.Errorf("load default chat model preferences: %w", err)
		}
		for _, preference := range batch {
			preferences[preference.UserID] = preference.DefaultChatModel
		}
	}
	return preferences, nil
}

func loadDefaultModelUserGroups(db *gorm.DB, userIDs []int) (map[int]string, error) {
	const batchSize = 500

	groups := make(map[int]string, len(userIDs))
	groupColumn := "`group`"
	if db.Dialector.Name() == "postgres" {
		groupColumn = `"group"`
	}
	for start := 0; start < len(userIDs); start += batchSize {
		end := start + batchSize
		if end > len(userIDs) {
			end = len(userIDs)
		}
		var users []model.User
		if err := db.Select("id, "+groupColumn).
			Where("id IN ?", userIDs[start:end]).Find(&users).Error; err != nil {
			return nil, fmt.Errorf("load default chat model user groups: %w", err)
		}
		for _, user := range users {
			groups[user.Id] = strings.TrimSpace(user.Group)
		}
	}
	return groups, nil
}

func defaultChatModelRoutable(db *gorm.DB, group string) (bool, error) {
	group = strings.TrimSpace(group)
	if group == "" {
		return false, nil
	}
	routable, err := mujianprovider.CatalogModelRoutableForGroupTx(db, newDefaultChatModel, group)
	if err != nil {
		return false, fmt.Errorf("verify %s route for user group %q: %w", newDefaultChatModel, group, err)
	}
	return routable, nil
}

func (plan defaultModelPlan) report() migrationReport {
	report := newMigrationReport(plan.Command, plan.DryRun)
	report.Version = defaultModelReportVersion
	report.Database = plan.Database
	report.DeploymentID = plan.DeploymentID
	report.OperationID = plan.OperationID
	report.SourceOperationID = plan.SourceOperationID
	report.SourceCommitDigest = plan.SourceCommitDigest
	for _, planned := range plan.Entries {
		change := planned.Change
		report.add(reportEntry{
			Kind: defaultModelReportKind, ID: strconv.Itoa(change.UserID), UserID: change.UserID,
			From: change.From, To: change.To, Status: planned.Status, Error: planned.Error,
		})
	}
	report.finish()
	return report
}

func readDefaultModelSnapshot(path, expectedDatabase, expectedDeploymentID string) (defaultModelSnapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return defaultModelSnapshot{}, fmt.Errorf("read default chat model snapshot: %w", err)
	}
	var report migrationReport
	if err = common.Unmarshal(data, &report); err != nil {
		return defaultModelSnapshot{}, fmt.Errorf("decode default chat model snapshot: %w", err)
	}
	if report.Version != defaultModelReportVersion || report.Command != defaultChatModelCommand || report.DryRun {
		return defaultModelSnapshot{}, fmt.Errorf("snapshot must be a version %d applied default-chat-model report", defaultModelReportVersion)
	}
	if expectedDatabase == "" || report.Database != expectedDatabase {
		return defaultModelSnapshot{}, fmt.Errorf("snapshot database %q does not match target database %q", report.Database, expectedDatabase)
	}
	if expectedDeploymentID == "" || report.DeploymentID != expectedDeploymentID {
		return defaultModelSnapshot{}, fmt.Errorf("snapshot deployment %q does not match target deployment %q", report.DeploymentID, expectedDeploymentID)
	}
	if err = validateDefaultModelOperationID(report.OperationID); err != nil {
		return defaultModelSnapshot{}, fmt.Errorf("snapshot operation_id: %w", err)
	}
	if report.SourceOperationID != "" || report.SourceCommitDigest != "" {
		return defaultModelSnapshot{}, errors.New("snapshot contains unexpected rollback source metadata")
	}
	expectedDigest, err := defaultModelReportDigest(report)
	if err != nil {
		return defaultModelSnapshot{}, err
	}
	if report.CommitDigest == "" || report.CommitDigest != expectedDigest {
		return defaultModelSnapshot{}, errors.New("snapshot commit digest does not match its contents")
	}
	seen := make(map[int]struct{}, len(report.Entries))
	for _, entry := range report.Entries {
		if entry.Kind != defaultModelReportKind || entry.UserID <= 0 || entry.ID != strconv.Itoa(entry.UserID) ||
			entry.From != legacyDefaultChatModel || entry.To != newDefaultChatModel {
			return defaultModelSnapshot{}, errors.New("snapshot contains an invalid default chat model entry")
		}
		if _, exists := seen[entry.UserID]; exists {
			return defaultModelSnapshot{}, fmt.Errorf("snapshot contains duplicate user_id %d", entry.UserID)
		}
		seen[entry.UserID] = struct{}{}
		switch entry.Status {
		case "updated", "skipped_changed", "skipped_missing", "skipped_unavailable":
			// These are the only terminal statuses emitted by an applied run;
			// the live assignment ledger, not this historical row, authorizes rollback.
		default:
			return defaultModelSnapshot{}, fmt.Errorf("snapshot contains unsupported status %q", entry.Status)
		}
	}
	return defaultModelSnapshot{OperationID: report.OperationID, CommitDigest: report.CommitDigest}, nil
}

func validateDefaultModelPlanIdentity(plan defaultModelPlan) error {
	if err := validateDefaultModelOperationID(plan.OperationID); err != nil {
		return fmt.Errorf("default model plan operation_id: %w", err)
	}
	if plan.Command == defaultChatModelCommand {
		if plan.SourceOperationID != "" || plan.SourceCommitDigest != "" {
			return errors.New("forward default model plan cannot contain rollback source metadata")
		}
		if len(plan.AutomaticUserIDs) != 0 {
			return errors.New("forward default model plan cannot contain a rollback ledger snapshot")
		}
		for _, entry := range plan.Entries {
			if entry.LedgerTracked {
				return errors.New("forward default model plan cannot contain rollback-ledger entries")
			}
		}
		return validateDefaultModelPlanChanges(plan, legacyDefaultChatModel, newDefaultChatModel)
	}
	if plan.Command != rollbackDefaultChatModelCommand {
		return fmt.Errorf("unsupported default model plan command %q", plan.Command)
	}
	if err := validateDefaultModelOperationID(plan.SourceOperationID); err != nil {
		return fmt.Errorf("rollback source operation_id: %w", err)
	}
	if plan.SourceCommitDigest == "" {
		return errors.New("rollback source commit digest is required")
	}
	if err := validateDefaultModelPlanChanges(plan, newDefaultChatModel, legacyDefaultChatModel); err != nil {
		return err
	}
	tracked := make([]int, 0, len(plan.Entries))
	for _, entry := range plan.Entries {
		if !entry.LedgerTracked {
			return errors.New("rollback plan contains an entry outside the automatic-assignment ledger")
		}
		tracked = append(tracked, entry.Change.UserID)
	}
	sort.Ints(tracked)
	want := append([]int(nil), plan.AutomaticUserIDs...)
	sort.Ints(want)
	if !equalDefaultModelUserIDs(tracked, want) {
		return errors.New("rollback plan does not match its automatic-assignment ledger snapshot")
	}
	return nil
}

func validateDefaultModelPlanChanges(plan defaultModelPlan, from, to string) error {
	seen := make(map[int]struct{}, len(plan.Entries))
	for _, entry := range plan.Entries {
		change := entry.Change
		if change.UserID <= 0 || change.From != from || change.To != to {
			return fmt.Errorf("%s plan contains an invalid default chat model change", plan.Command)
		}
		if _, exists := seen[change.UserID]; exists {
			return fmt.Errorf("%s plan contains duplicate user_id %d", plan.Command, change.UserID)
		}
		seen[change.UserID] = struct{}{}
	}
	return nil
}

func equalDefaultModelUserIDs(left, right []int) bool {
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

func validateDefaultModelOperationID(operationID string) error {
	parsed, err := uuid.Parse(operationID)
	if err != nil || parsed.String() != operationID {
		return errors.New("must be a canonical UUID")
	}
	return nil
}

func sealDefaultModelReport(report *migrationReport) error {
	digest, err := defaultModelReportDigest(*report)
	if err != nil {
		return err
	}
	report.CommitDigest = digest
	return nil
}

func defaultModelReportDigest(report migrationReport) (string, error) {
	report.CommitDigest = ""
	data, err := common.Marshal(report)
	if err != nil {
		return "", fmt.Errorf("marshal default chat model snapshot for digest: %w", err)
	}
	digest := sha256.Sum256(data)
	return fmt.Sprintf("sha256:%x", digest), nil
}

func defaultModelCommitMarkerKey(operationID string) string {
	return defaultModelCommitOptionPrefix + operationID
}

func defaultModelCommittedValue(commitDigest string) string {
	return defaultModelCommittedPrefix + commitDigest
}

func defaultModelActiveCutoverValue(operationID, commitDigest string) string {
	return operationID + ":" + commitDigest
}

func requireNoDefaultModelActiveCutover(tx *gorm.DB) error {
	var option model.Option
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&option, "key = ?", defaultModelActiveCutoverKey).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check active default model cutover: %w", err)
	}
	return errors.New("an active default model cutover already exists; roll it back before applying another")
}

func requireDefaultModelActiveCutover(ctx context.Context, db *gorm.DB, snapshot defaultModelSnapshot) error {
	var option model.Option
	err := db.WithContext(ctx).First(&option, "key = ?", defaultModelActiveCutoverKey).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errors.New("snapshot has no active default model cutover")
	}
	if err != nil {
		return fmt.Errorf("load active default model cutover: %w", err)
	}
	if option.Value != defaultModelActiveCutoverValue(snapshot.OperationID, snapshot.CommitDigest) {
		return errors.New("snapshot does not match the active default model cutover")
	}
	return nil
}

func lockDefaultModelActiveCutover(tx *gorm.DB, plan defaultModelPlan) error {
	var option model.Option
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&option, "key = ?", defaultModelActiveCutoverKey).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errors.New("rollback source has no active default model cutover")
	}
	if err != nil {
		return fmt.Errorf("lock active default model cutover: %w", err)
	}
	if option.Value != defaultModelActiveCutoverValue(plan.SourceOperationID, plan.SourceCommitDigest) {
		return errors.New("rollback source does not match the active default model cutover")
	}
	return nil
}

func createDefaultModelActiveCutover(tx *gorm.DB, report migrationReport) error {
	option := model.Option{
		Key:   defaultModelActiveCutoverKey,
		Value: defaultModelActiveCutoverValue(report.OperationID, report.CommitDigest),
	}
	if err := tx.Create(&option).Error; err != nil {
		return fmt.Errorf("create active default model cutover: %w", err)
	}
	return nil
}

func consumeDefaultModelActiveCutover(tx *gorm.DB, plan defaultModelPlan) error {
	result := tx.Where(
		"key = ? AND value = ?",
		defaultModelActiveCutoverKey,
		defaultModelActiveCutoverValue(plan.SourceOperationID, plan.SourceCommitDigest),
	).Delete(&model.Option{})
	if result.Error != nil {
		return fmt.Errorf("consume active default model cutover: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return errors.New("active default model cutover changed before commit")
	}
	return nil
}

func requireDefaultModelCommitMarker(ctx context.Context, db *gorm.DB, snapshot defaultModelSnapshot) error {
	var option model.Option
	err := db.WithContext(ctx).First(&option, "key = ?", defaultModelCommitMarkerKey(snapshot.OperationID)).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errors.New("snapshot has no committed database marker")
	}
	if err != nil {
		return fmt.Errorf("load default model commit marker: %w", err)
	}
	if option.Value != defaultModelCommittedValue(snapshot.CommitDigest) {
		return errors.New("snapshot commit marker is invalid or already consumed")
	}
	return nil
}

func lockDefaultModelSourceCommitMarker(tx *gorm.DB, plan defaultModelPlan) error {
	var option model.Option
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		First(&option, "key = ?", defaultModelCommitMarkerKey(plan.SourceOperationID)).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errors.New("rollback source has no committed database marker")
	}
	if err != nil {
		return fmt.Errorf("lock rollback source commit marker: %w", err)
	}
	if option.Value != defaultModelCommittedValue(plan.SourceCommitDigest) {
		return errors.New("rollback source commit marker is invalid or already consumed")
	}
	return nil
}

func createDefaultModelCommitMarker(tx *gorm.DB, report migrationReport) error {
	option := model.Option{
		Key:   defaultModelCommitMarkerKey(report.OperationID),
		Value: defaultModelCommittedValue(report.CommitDigest),
	}
	if err := tx.Create(&option).Error; err != nil {
		return fmt.Errorf("create default model commit marker: %w", err)
	}
	return nil
}

func consumeDefaultModelSourceCommitMarker(tx *gorm.DB, plan defaultModelPlan, rollback migrationReport) error {
	consumedValue := defaultModelRolledBackPrefix + rollback.OperationID + ":" + rollback.CommitDigest
	result := tx.Model(&model.Option{}).
		Where("key = ? AND value = ?", defaultModelCommitMarkerKey(plan.SourceOperationID), defaultModelCommittedValue(plan.SourceCommitDigest)).
		Update("value", consumedValue)
	if result.Error != nil {
		return fmt.Errorf("consume rollback source commit marker: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return errors.New("rollback source commit marker changed before commit")
	}
	return nil
}
