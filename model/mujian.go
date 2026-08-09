package model

import "gorm.io/gorm"

const MujianInternalTokenName = "mujian-internal"

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
	ID              string         `json:"id" gorm:"type:varchar(36);primaryKey"`
	ProjectID       string         `json:"project_id" gorm:"type:varchar(36);index;not null"`
	SceneID         string         `json:"scene_id" gorm:"type:varchar(36);index;not null"`
	Sequence        int            `json:"sequence" gorm:"not null"`
	ShotType        string         `json:"shot_type" gorm:"type:varchar(32)"`
	DurationSeconds int            `json:"duration_seconds" gorm:"not null;default:3"`
	Prompt          string         `json:"prompt" gorm:"type:text"`
	Seedance        string         `json:"seedance" gorm:"type:text"`
	AspectRatio     string         `json:"aspect_ratio" gorm:"type:varchar(16);default:'9:16'"`
	Model           string         `json:"model" gorm:"type:varchar(120)"`
	ResultURL       string         `json:"result_url" gorm:"type:text"`
	Status          string         `json:"status" gorm:"type:varchar(24);default:'draft'"`
	Version         int            `json:"version" gorm:"not null;default:1"`
	CreatedAt       int64          `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt       int64          `json:"updated_at" gorm:"autoUpdateTime"`
	DeletedAt       gorm.DeletedAt `json:"-" gorm:"index"`
}

type MujianAgentMessage struct {
	ID             string `json:"id" gorm:"type:varchar(36);primaryKey"`
	ProjectID      string `json:"project_id" gorm:"type:varchar(36);index;not null"`
	UserID         int    `json:"user_id" gorm:"index;not null"`
	Role           string `json:"role" gorm:"type:varchar(16);not null"`
	Content        string `json:"content" gorm:"type:text;not null"`
	Skill          string `json:"skill" gorm:"type:varchar(80)"`
	Proposal       string `json:"proposal" gorm:"type:text"`
	PreviousValues string `json:"-" gorm:"type:text"`
	ApplyStatus    string `json:"apply_status" gorm:"type:varchar(24);default:'none'"`
	RelayRequestID string `json:"relay_request_id" gorm:"type:varchar(64);index"`
	CreatedAt      int64  `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt      int64  `json:"updated_at" gorm:"autoUpdateTime"`
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
	var projectIDs []string
	if err := tx.Unscoped().Model(&MujianProject{}).Where("user_id = ?", userID).Pluck("id", &projectIDs).Error; err != nil {
		return err
	}
	if len(projectIDs) > 0 {
		if err := tx.Unscoped().Where("project_id IN ?", projectIDs).Delete(&MujianShot{}).Error; err != nil {
			return err
		}
		if err := tx.Unscoped().Where("project_id IN ?", projectIDs).Delete(&MujianScene{}).Error; err != nil {
			return err
		}
		if err := tx.Unscoped().Where("project_id IN ?", projectIDs).Delete(&MujianAgentMessage{}).Error; err != nil {
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
