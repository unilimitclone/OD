package db

import (
	"time"

	"github.com/alist-org/alist/v3/internal/model"
	"gorm.io/gorm"
)

func GetShareByShareID(shareID string) (*model.Share, error) {
	var share model.Share
	if err := db.Where("share_id = ?", shareID).Take(&share).Error; err != nil {
		return nil, err
	}
	return &share, nil
}

func ShareIDExists(shareID string) bool {
	var count int64
	if err := db.Model(&model.Share{}).Where("share_id = ?", shareID).Count(&count).Error; err != nil {
		return false
	}
	return count > 0
}

func CreateShare(share *model.Share) error {
	return db.Create(share).Error
}

func UpdateShare(share *model.Share) error {
	return db.Save(share).Error
}

func GetSharesByCreator(creatorID uint, pageIndex, pageSize int) (shares []model.Share, count int64, err error) {
	tx := db.Model(&model.Share{}).Where("creator_id = ?", creatorID)
	err = tx.Count(&count).Error
	if err != nil {
		return nil, 0, err
	}
	err = tx.Order("created_at desc").Offset((pageIndex - 1) * pageSize).Limit(pageSize).Find(&shares).Error
	return
}

func DeleteShareByShareID(creatorID uint, shareID string) error {
	return db.Where("creator_id = ? AND share_id = ?", creatorID, shareID).Delete(&model.Share{}).Error
}

func TouchShareView(shareID string) error {
	now := time.Now()
	return db.Model(&model.Share{}).
		Where("share_id = ?", shareID).
		UpdateColumns(map[string]interface{}{
			"last_access_at": now,
			"view_count":     gorm.Expr("view_count + ?", 1),
		}).Error
}

func TouchShareDownload(shareID string) error {
	now := time.Now()
	return db.Model(&model.Share{}).
		Where("share_id = ?", shareID).
		UpdateColumns(map[string]interface{}{
			"last_access_at": now,
			"download_count": gorm.Expr("download_count + ?", 1),
		}).Error
}
