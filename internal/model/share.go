package model

import "time"

type Share struct {
	ID            uint       `json:"id" gorm:"primaryKey"`
	ShareID       string     `json:"share_id" gorm:"uniqueIndex;size:32;not null"`
	CreatorID     uint       `json:"creator_id" gorm:"index;not null"`
	Name          string     `json:"name" gorm:"size:255;not null"`
	RootPath      string     `json:"root_path" gorm:"size:4096;not null"`
	IsDir         bool       `json:"is_dir"`
	PasswordHash  string     `json:"-" gorm:"size:64"`
	PasswordSalt  string     `json:"-" gorm:"size:32"`
	AllowPreview  bool       `json:"allow_preview" gorm:"default:true"`
	AllowDownload bool       `json:"allow_download" gorm:"default:true"`
	Enabled       bool       `json:"enabled" gorm:"default:true;index"`
	ViewCount     int64      `json:"view_count"`
	DownloadCount int64      `json:"download_count"`
	LastAccessAt  *time.Time `json:"last_access_at"`
	ExpiresAt     *time.Time `json:"expires_at"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

func (s Share) HasPassword() bool {
	return s.PasswordHash != ""
}

func (s Share) IsExpired(now time.Time) bool {
	return s.ExpiresAt != nil && s.ExpiresAt.Before(now)
}
