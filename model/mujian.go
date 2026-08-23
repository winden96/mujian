package model

import (
	"errors"
	"time"

	"github.com/QuantumNous/new-api/constant"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	MujianInternalTokenName = "mujian-internal"
	// Reference loading, provider dispatch, remote result materialization, and
	// OBS upload are independently bounded phases. The execution context covers
	// all four; the durable lease includes a final database-settlement margin.
	MujianImageGenerationRequestTimeout   = 180 * time.Second
	MujianImageGenerationExecutionTimeout = 4 * MujianImageGenerationRequestTimeout
	MujianImageGenerationLeaseTimeout     = MujianImageGenerationExecutionTimeout + 30*time.Second
	mujianImageGenerationLeaseStaleReason = "生图任务在服务重启后中断，请重新生成"
)

var ErrMujianImageGenerationActive = errors.New("生图任务正在进行中，请稍后再删除")

// ClaimMujianProjectWrite serializes project-scoped writes with project and
// user deletion. The timestamp changes on every claim so RowsAffected is
// reliable across SQLite, MySQL, and PostgreSQL.
func ClaimMujianProjectWrite(tx *gorm.DB, userID int, projectID string) (bool, error) {
	now := time.Now().Unix()
	result := tx.Model(&MujianProject{}).
		Where("id = ? AND user_id = ?", projectID, userID).
		UpdateColumn("updated_at", gorm.Expr("CASE WHEN updated_at >= ? THEN updated_at + 1 ELSE ? END", now, now))
	return result.RowsAffected == 1, result.Error
}

// ClaimMujianUserWrite is the user-level barrier for onboarding, project
// creation, and user deletion. User has no lock-version column, so existence
// is checked separately instead of relying on MySQL's no-op RowsAffected.
func ClaimMujianUserWrite(tx *gorm.DB, userID int) (bool, error) {
	result := tx.Model(&User{}).Where("id = ?", userID).UpdateColumn("status", gorm.Expr("status"))
	if result.Error != nil {
		return false, result.Error
	}
	var count int64
	if err := tx.Model(&User{}).Where("id = ?", userID).Count(&count).Error; err != nil {
		return false, err
	}
	return count == 1, nil
}

func claimMujianUserProjectsWrite(tx *gorm.DB, userID int) error {
	now := time.Now().Unix()
	return tx.Model(&MujianProject{}).
		Where("user_id = ?", userID).
		UpdateColumn("updated_at", gorm.Expr("CASE WHEN updated_at >= ? THEN updated_at + 1 ELSE ? END", now, now)).Error
}

// GuardMujianImageGenerationLeases keeps deletion linearizable with the
// external image dispatch. A running generation paired with an in-progress
// task is a durable lease: deletion must either observe it as stale and fail
// it atomically, or return a recognizable conflict without deleting data.
func GuardMujianImageGenerationLeases(tx *gorm.DB, userID int, projectIDs []string, now time.Time) error {
	if len(projectIDs) == 0 {
		return nil
	}
	type imageLease struct {
		GenerationID string
		TaskID       string
		StartTime    int64
	}
	var leases []imageLease
	if err := tx.Table("mujian_image_generations AS generations").
		Select("generations.id AS generation_id, tasks.task_id, tasks.start_time").
		Joins("JOIN tasks ON tasks.task_id = generations.task_id AND tasks.user_id = generations.user_id").
		Where(
			"generations.user_id = ? AND generations.project_id IN ? AND generations.status = ? AND tasks.platform = ? AND tasks.status = ?",
			userID, projectIDs, "running", constant.TaskPlatformMujianImage, TaskStatusInProgress,
		).
		Find(&leases).Error; err != nil {
		return err
	}
	staleBefore := now.Add(-MujianImageGenerationLeaseTimeout).Unix()
	staleGenerationIDs := make([]string, 0, len(leases))
	staleTaskIDs := make([]string, 0, len(leases))
	for _, lease := range leases {
		if lease.StartTime > 0 && lease.StartTime <= staleBefore {
			staleGenerationIDs = append(staleGenerationIDs, lease.GenerationID)
			staleTaskIDs = append(staleTaskIDs, lease.TaskID)
		}
	}
	if len(staleTaskIDs) > 0 {
		if err := tx.Model(&Task{}).
			Where("user_id = ? AND task_id IN ? AND platform = ? AND status = ?", userID, staleTaskIDs, constant.TaskPlatformMujianImage, TaskStatusInProgress).
			Updates(map[string]interface{}{
				"status": TaskStatusFailure, "progress": "100%", "finish_time": now.Unix(),
				"fail_reason": mujianImageGenerationLeaseStaleReason,
			}).Error; err != nil {
			return err
		}
		if err := tx.Model(&MujianImageGeneration{}).
			Where("id IN ? AND user_id = ? AND status = ?", staleGenerationIDs, userID, "running").
			Updates(map[string]interface{}{
				"status": "failed", "error": mujianImageGenerationLeaseStaleReason,
			}).Error; err != nil {
			return err
		}
	}
	var activeCount int64
	if err := tx.Table("mujian_image_generations AS generations").
		Joins("JOIN tasks ON tasks.task_id = generations.task_id AND tasks.user_id = generations.user_id").
		Where(
			"generations.user_id = ? AND generations.project_id IN ? AND generations.status = ? AND tasks.platform = ? AND tasks.status = ?",
			userID, projectIDs, "running", constant.TaskPlatformMujianImage, TaskStatusInProgress,
		).
		Count(&activeCount).Error; err != nil {
		return err
	}
	if activeCount > 0 {
		return ErrMujianImageGenerationActive
	}
	return nil
}

// MujianProject is the user-owned root aggregate for the creative workspace.
type MujianProject struct {
	ID             string         `json:"id" gorm:"type:varchar(36);primaryKey"`
	UserID         int            `json:"user_id" gorm:"index;not null"`
	Title          string         `json:"title" gorm:"type:varchar(120);not null"`
	Type           string         `json:"type" gorm:"type:varchar(32);not null;default:'短剧'"`
	Synopsis       string         `json:"synopsis" gorm:"type:text"`
	CurrentEpisode int            `json:"current_episode" gorm:"not null;default:1"`
	Progress       int            `json:"progress" gorm:"not null;default:0"`
	CoverURL       string         `json:"cover_url" gorm:"type:text"`
	CreatedAt      int64          `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt      int64          `json:"updated_at" gorm:"autoUpdateTime"`
	DeletedAt      gorm.DeletedAt `json:"-" gorm:"index"`
}

type MujianScene struct {
	ID          string         `json:"id" gorm:"type:varchar(36);primaryKey"`
	ProjectID   string         `json:"project_id" gorm:"type:varchar(36);index;not null"`
	Episode     int            `json:"episode" gorm:"not null;default:1"`
	SceneNumber int            `json:"scene_number" gorm:"not null"`
	Title       string         `json:"title" gorm:"type:varchar(160);not null"`
	Environment string         `json:"environment" gorm:"type:text"`
	Action      string         `json:"action" gorm:"type:text"`
	Dialogues   string         `json:"dialogues" gorm:"type:text"`
	Version     int            `json:"version" gorm:"not null;default:1"`
	CreatedAt   int64          `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt   int64          `json:"updated_at" gorm:"autoUpdateTime"`
	DeletedAt   gorm.DeletedAt `json:"-" gorm:"index"`
}

type MujianShot struct {
	ID               string         `json:"id" gorm:"type:varchar(36);primaryKey"`
	ProjectID        string         `json:"project_id" gorm:"type:varchar(36);index;not null"`
	SceneID          string         `json:"scene_id" gorm:"type:varchar(36);index;not null"`
	Sequence         int            `json:"sequence" gorm:"not null"`
	ShotType         string         `json:"shot_type" gorm:"type:varchar(32)"`
	DurationSeconds  int            `json:"duration_seconds" gorm:"not null;default:3"`
	Prompt           string         `json:"prompt" gorm:"type:text"`
	Seedance         string         `json:"seedance" gorm:"type:text"`
	AspectRatio      string         `json:"aspect_ratio" gorm:"type:varchar(16);default:'9:16'"`
	Model            string         `json:"model" gorm:"type:varchar(120)"`
	ResultURL        string         `json:"result_url" gorm:"type:text"`
	ResultObjectKey  string         `json:"-" gorm:"type:varchar(191);index"`
	ResultSizeBytes  int64          `json:"-" gorm:"not null;default:0"`
	ResultSHA256     string         `json:"-" gorm:"type:varchar(64)"`
	ResultETag       string         `json:"-" gorm:"column:result_etag;type:varchar(191)"`
	Status           string         `json:"status" gorm:"type:varchar(24);default:'draft'"`
	GenerationTaskID string         `json:"generation_task_id" gorm:"type:varchar(191);index"`
	GenerationError  string         `json:"generation_error" gorm:"type:text"`
	Version          int            `json:"version" gorm:"not null;default:1"`
	CreatedAt        int64          `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt        int64          `json:"updated_at" gorm:"autoUpdateTime"`
	DeletedAt        gorm.DeletedAt `json:"-" gorm:"index"`
}

type MujianAgentSession struct {
	ID        string `json:"id" gorm:"type:varchar(36);primaryKey"`
	ProjectID string `json:"project_id" gorm:"type:varchar(36);index;index:idx_mujian_agent_sessions_owner,priority:2;not null"`
	UserID    int    `json:"user_id" gorm:"index;index:idx_mujian_agent_sessions_owner,priority:1;not null"`
	Title     string `json:"title" gorm:"type:varchar(120);not null"`
	Revision  int    `json:"revision" gorm:"not null;default:1"`
	CreatedAt int64  `json:"created_at" gorm:"autoCreateTime:milli"`
	UpdatedAt int64  `json:"updated_at" gorm:"index;autoUpdateTime:milli"`
}

type MujianAgentMessage struct {
	ID             string  `json:"id" gorm:"type:varchar(36);primaryKey"`
	ProjectID      string  `json:"project_id" gorm:"type:varchar(36);index;index:idx_mujian_agent_messages_session,priority:2;not null"`
	UserID         int     `json:"user_id" gorm:"index;index:idx_mujian_agent_messages_session,priority:1;not null"`
	SessionID      string  `json:"session_id" gorm:"type:varchar(36);index;index:idx_mujian_agent_messages_session,priority:3;not null;default:''"`
	Role           string  `json:"role" gorm:"type:varchar(16);not null"`
	Content        string  `json:"content" gorm:"type:text;not null"`
	Reasoning      *string `json:"reasoning,omitempty" gorm:"type:text"`
	Skill          string  `json:"skill" gorm:"type:varchar(80)"`
	Mode           string  `json:"mode" gorm:"type:varchar(16);not null;default:'execute'"`
	Proposal       string  `json:"proposal" gorm:"type:text"`
	PreviousValues string  `json:"-" gorm:"type:text"`
	ApplyStatus    string  `json:"apply_status" gorm:"type:varchar(24);default:'none'"`
	RelayRequestID string  `json:"relay_request_id" gorm:"type:varchar(64);index"`
	CreatedAt      int64   `json:"created_at" gorm:"index:idx_mujian_agent_messages_session,priority:4;autoCreateTime:milli"`
	UpdatedAt      int64   `json:"updated_at" gorm:"autoUpdateTime"`
}

func migrateMujianAgentSessions() error {
	var projects []MujianProject
	if err := DB.Find(&projects).Error; err != nil {
		return err
	}
	for _, project := range projects {
		if err := migrateMujianProjectAgentSessions(project); err != nil {
			return err
		}
	}
	return nil
}

func migratedMujianAgentSessionID(projectID, kind string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("mujian-agent-session:"+kind+":"+projectID)).String()
}

const mujianUnixMillisThreshold int64 = 100_000_000_000

func scanMujianLegacyActivity(query *gorm.DB) (count, latestMillis int64, err error) {
	var activity struct {
		Count        int64 `gorm:"column:row_count"`
		LatestMillis int64 `gorm:"column:latest_millis"`
	}
	// Legacy seconds and current milliseconds can coexist. Normalize each value
	// inside the aggregate so a raw millisecond value cannot mask newer seconds.
	err = query.Select(
		"COUNT(*) AS row_count, COALESCE(MAX(CASE WHEN created_at > 0 AND created_at < ? THEN created_at * 1000 ELSE created_at END), 0) AS latest_millis",
		mujianUnixMillisThreshold,
	).Scan(&activity).Error
	return activity.Count, activity.LatestMillis, err
}

func migrateMujianProjectAgentSessions(project MujianProject) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var lockedProject MujianProject
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").
			Where("id = ? AND user_id = ?", project.ID, project.UserID).First(&lockedProject).Error; err != nil {
			return err
		}

		legacyMessageCount, latestMessageAt, err := scanMujianLegacyActivity(tx.Model(&MujianAgentMessage{}).
			Where("project_id = ? AND user_id = ? AND session_id = ?", project.ID, project.UserID, ""))
		if err != nil {
			return err
		}
		legacyImageCount, latestImageAt, err := scanMujianLegacyActivity(tx.Model(&MujianImageGeneration{}).
			Where("project_id = ? AND user_id = ? AND session_id = ?", project.ID, project.UserID, ""))
		if err != nil {
			return err
		}

		if legacyMessageCount > 0 || legacyImageCount > 0 {
			session, err := legacyMujianAgentSession(tx, project)
			if err != nil {
				return err
			}
			if err := tx.Model(&MujianAgentMessage{}).
				Where("project_id = ? AND user_id = ? AND session_id = ?", project.ID, project.UserID, "").
				Update("session_id", session.ID).Error; err != nil {
				return err
			}
			if err := tx.Model(&MujianImageGeneration{}).
				Where("project_id = ? AND user_id = ? AND session_id = ?", project.ID, project.UserID, "").
				Update("session_id", session.ID).Error; err != nil {
				return err
			}
			latestActivityAt := max(latestMessageAt, latestImageAt)
			if latestActivityAt > 0 {
				if err := tx.Model(&MujianAgentSession{}).
					Where("id = ? AND project_id = ? AND user_id = ?", session.ID, project.ID, project.UserID).
					UpdateColumn("updated_at", gorm.Expr(
						"CASE WHEN updated_at >= ? THEN updated_at ELSE ? END", latestActivityAt, latestActivityAt,
					)).Error; err != nil {
					return err
				}
			}
		}

		var sessionCount int64
		if err := tx.Model(&MujianAgentSession{}).
			Where("project_id = ? AND user_id = ?", project.ID, project.UserID).
			Count(&sessionCount).Error; err != nil {
			return err
		}
		if sessionCount != 0 {
			return nil
		}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&MujianAgentSession{
			ID: migratedMujianAgentSessionID(project.ID, "default"), ProjectID: project.ID, UserID: project.UserID,
			Title: "新会话", Revision: 1,
		}).Error
	})
}

func legacyMujianAgentSession(tx *gorm.DB, project MujianProject) (*MujianAgentSession, error) {
	var session MujianAgentSession
	err := tx.Where("project_id = ? AND user_id = ? AND title = ?", project.ID, project.UserID, "历史会话").
		Order("created_at, id").First(&session).Error
	if err == nil {
		return &session, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	emptyDefaults, err := emptyDefaultMujianAgentSessions(tx, project)
	if err != nil {
		return nil, err
	}
	if len(emptyDefaults) == 1 {
		session = emptyDefaults[0]
		result := tx.Model(&MujianAgentSession{}).
			Where("id = ? AND project_id = ? AND user_id = ? AND title = ?", session.ID, project.ID, project.UserID, "新会话").
			Update("title", "历史会话")
		if result.Error != nil {
			return nil, result.Error
		}
		if result.RowsAffected == 1 {
			session.Title = "历史会话"
			return &session, nil
		}
	}

	session = MujianAgentSession{
		ID: migratedMujianAgentSessionID(project.ID, "history"), ProjectID: project.ID, UserID: project.UserID,
		Title: "历史会话", Revision: 1,
	}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&session).Error; err != nil {
		return nil, err
	}
	if err := tx.Where("id = ? AND project_id = ? AND user_id = ?", session.ID, project.ID, project.UserID).
		First(&session).Error; err != nil {
		return nil, err
	}
	return &session, nil
}

func emptyDefaultMujianAgentSessions(tx *gorm.DB, project MujianProject) ([]MujianAgentSession, error) {
	var candidates []MujianAgentSession
	if err := tx.Where("project_id = ? AND user_id = ? AND title = ?", project.ID, project.UserID, "新会话").
		Order("created_at, id").Find(&candidates).Error; err != nil {
		return nil, err
	}
	empty := make([]MujianAgentSession, 0, len(candidates))
	for _, candidate := range candidates {
		var messageCount, imageCount int64
		if err := tx.Model(&MujianAgentMessage{}).Where("session_id = ?", candidate.ID).Count(&messageCount).Error; err != nil {
			return nil, err
		}
		if err := tx.Model(&MujianImageGeneration{}).Where("session_id = ?", candidate.ID).Count(&imageCount).Error; err != nil {
			return nil, err
		}
		if messageCount == 0 && imageCount == 0 {
			empty = append(empty, candidate)
		}
	}
	return empty, nil
}

// MujianImageGeneration is a session-scoped standalone image job.
type MujianImageGeneration struct {
	ID          string `json:"id" gorm:"type:varchar(36);primaryKey"`
	ProjectID   string `json:"project_id" gorm:"type:varchar(36);index;index:idx_mujian_image_generations_session,priority:2;not null"`
	UserID      int    `json:"-" gorm:"index;index:idx_mujian_image_generations_session,priority:1;not null"`
	SessionID   string `json:"session_id" gorm:"type:varchar(36);index;index:idx_mujian_image_generations_session,priority:3;not null;default:''"`
	Prompt      string `json:"prompt" gorm:"type:text;not null"`
	Engine      string `json:"engine" gorm:"type:varchar(16);not null"`
	ModelID     string `json:"model_id" gorm:"type:varchar(120);not null"`
	AspectRatio string `json:"aspect_ratio" gorm:"type:varchar(16);not null"`
	Status      string `json:"status" gorm:"type:varchar(24);index;not null"`
	TaskID      string `json:"task_id" gorm:"type:varchar(191);uniqueIndex;not null"`
	// ResultData and ResultURL remain for rows created before OBS storage.
	// Leaving ResultData implicit lets GORM select longblob/bytea/blob.
	ResultData      []byte `json:"-" gorm:"column:result_data"`
	ResultMIMEType  string `json:"-" gorm:"type:varchar(64);column:result_mime_type"`
	ResultURL       string `json:"-" gorm:"type:text"`
	ResultObjectKey string `json:"-" gorm:"type:varchar(191);index"`
	ResultSizeBytes int64  `json:"-" gorm:"not null;default:0"`
	ResultSHA256    string `json:"-" gorm:"type:varchar(64)"`
	ResultETag      string `json:"-" gorm:"column:result_etag;type:varchar(191)"`
	Error           string `json:"error,omitempty" gorm:"type:text"`
	RelayRequestID  string `json:"relay_request_id,omitempty" gorm:"type:varchar(64);index"`
	CreatedAt       int64  `json:"created_at" gorm:"index;index:idx_mujian_image_generations_session,priority:4;autoCreateTime:milli"`
	UpdatedAt       int64  `json:"updated_at" gorm:"autoUpdateTime:milli"`
}

// MujianImageReference stores the immutable ordered inputs for a generation.
// Data contains legacy database-backed content and is never serialized in
// workspace or generation API responses. OBS-backed rows leave it empty.
type MujianImageReference struct {
	ID              string `json:"id" gorm:"type:varchar(36);primaryKey"`
	GenerationID    string `json:"-" gorm:"type:varchar(36);index;not null"`
	Position        int    `json:"position" gorm:"not null"`
	Name            string `json:"name" gorm:"type:varchar(255);not null"`
	MIMEType        string `json:"mime_type" gorm:"type:varchar(64);not null"`
	SizeBytes       int64  `json:"size_bytes" gorm:"not null"`
	Data            []byte `json:"-"`
	ObjectKey       string `json:"-" gorm:"type:varchar(191);index"`
	ObjectSizeBytes int64  `json:"-" gorm:"not null;default:0"`
	ObjectSHA256    string `json:"-" gorm:"type:varchar(64)"`
	ObjectETag      string `json:"-" gorm:"column:object_etag;type:varchar(191)"`
	CreatedAt       int64  `json:"-" gorm:"autoCreateTime"`
}

type MujianUserPreference struct {
	UserID            int    `json:"user_id" gorm:"primaryKey"`
	DefaultChatModel  string `json:"default_chat_model" gorm:"type:varchar(120);not null"`
	DefaultImageModel string `json:"default_image_model" gorm:"type:varchar(120);not null"`
	EnabledSkills     string `json:"enabled_skills" gorm:"type:text"`
	Onboarded         bool   `json:"onboarded" gorm:"not null;default:false"`
	CreatedAt         int64  `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt         int64  `json:"updated_at" gorm:"autoUpdateTime"`
}

func deleteMujianUserData(tx *gorm.DB, userID int) error {
	claimed, err := ClaimMujianUserWrite(tx, userID)
	if err != nil {
		return err
	}
	if !claimed {
		return gorm.ErrRecordNotFound
	}
	// Claim active projects before reading or deleting child rows. Project-scoped
	// writers use the same project-first lock order and will fail their owner
	// claim after this transaction deletes the project.
	if err := claimMujianUserProjectsWrite(tx, userID); err != nil {
		return err
	}
	var projectIDs []string
	if err := tx.Unscoped().Model(&MujianProject{}).Where("user_id = ?", userID).Pluck("id", &projectIDs).Error; err != nil {
		return err
	}
	if err := GuardMujianImageGenerationLeases(tx, userID, projectIDs, time.Now()); err != nil {
		return err
	}
	if err := tx.Where("user_id = ? AND platform = ?", userID, constant.TaskPlatformMujianImage).Delete(&Task{}).Error; err != nil {
		return err
	}
	if len(projectIDs) > 0 {
		var generationIDs []string
		if err := tx.Model(&MujianImageGeneration{}).Where("project_id IN ?", projectIDs).Pluck("id", &generationIDs).Error; err != nil {
			return err
		}
		objectKeys := make([]string, 0)
		var generationObjectKeys []string
		if err := tx.Model(&MujianImageGeneration{}).
			Where("project_id IN ? AND result_object_key <> ?", projectIDs, "").
			Pluck("result_object_key", &generationObjectKeys).Error; err != nil {
			return err
		}
		objectKeys = append(objectKeys, generationObjectKeys...)
		if len(generationIDs) > 0 {
			var referenceObjectKeys []string
			if err := tx.Model(&MujianImageReference{}).
				Where("generation_id IN ? AND object_key <> ?", generationIDs, "").
				Pluck("object_key", &referenceObjectKeys).Error; err != nil {
				return err
			}
			objectKeys = append(objectKeys, referenceObjectKeys...)
		}
		var shotObjectKeys []string
		if err := tx.Unscoped().Model(&MujianShot{}).
			Where("project_id IN ? AND result_object_key <> ?", projectIDs, "").
			Pluck("result_object_key", &shotObjectKeys).Error; err != nil {
			return err
		}
		objectKeys = append(objectKeys, shotObjectKeys...)
		if err := EnqueueMujianObjectDeletes(tx, objectKeys); err != nil {
			return err
		}
		if len(generationIDs) > 0 {
			if err := tx.Where("generation_id IN ?", generationIDs).Delete(&MujianImageReference{}).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("project_id IN ?", projectIDs).Delete(&MujianImageGeneration{}).Error; err != nil {
			return err
		}
		if err := tx.Unscoped().Where("project_id IN ?", projectIDs).Delete(&MujianShot{}).Error; err != nil {
			return err
		}
		if err := tx.Unscoped().Where("project_id IN ?", projectIDs).Delete(&MujianScene{}).Error; err != nil {
			return err
		}
		if err := tx.Unscoped().Where("project_id IN ?", projectIDs).Delete(&MujianAgentMessage{}).Error; err != nil {
			return err
		}
		if err := tx.Where("project_id IN ?", projectIDs).Delete(&MujianAgentSession{}).Error; err != nil {
			return err
		}
	}
	if err := tx.Unscoped().Where("user_id = ?", userID).Delete(&MujianProject{}).Error; err != nil {
		return err
	}
	if err := tx.Unscoped().Where("user_id = ?", userID).Delete(&MujianUserPreference{}).Error; err != nil {
		return err
	}
	return tx.Unscoped().Where("user_id = ? AND name = ?", userID, MujianInternalTokenName).Delete(&Token{}).Error
}
