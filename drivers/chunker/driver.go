package chunker

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"path"
	"strconv"
	"time"

	"github.com/alist-org/alist/v3/internal/driver"
	"github.com/alist-org/alist/v3/internal/errs"
	"github.com/alist-org/alist/v3/internal/fs"
	"github.com/alist-org/alist/v3/internal/model"
	"github.com/alist-org/alist/v3/internal/op"
	"github.com/alist-org/alist/v3/internal/stream"
	"github.com/alist-org/alist/v3/pkg/http_range"
	"github.com/alist-org/alist/v3/pkg/utils"
)

func (d *Chunker) Config() driver.Config {
	return config
}

func (d *Chunker) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *Chunker) Init(ctx context.Context) error {
	if d.ChunkSize == 0 {
		d.ChunkSize = defaultChunkSize
	}
	if d.StartFrom == 0 {
		d.StartFrom = defaultStartFrom
	}
	d.NameFormat = utils.GetNoneEmpty(d.NameFormat, defaultChunkNameFmt)
	d.MetaFormat = utils.GetNoneEmpty(d.MetaFormat, defaultMetaFormat)
	d.HashType = utils.GetNoneEmpty(d.HashType, defaultHashType)

	if err := d.setChunkNameFormat(d.NameFormat); err != nil {
		return fmt.Errorf("invalid name_format: %w", err)
	}
	if err := d.validateOptions(); err != nil {
		return err
	}

	storage, err := fs.GetStorage(d.RemotePath, &fs.GetStoragesArgs{})
	if err != nil {
		return fmt.Errorf("can't find remote storage: %w", err)
	}
	d.remoteStorage = storage
	return nil
}

func (d *Chunker) Drop(ctx context.Context) error {
	d.remoteStorage = nil
	return nil
}

func (d *Chunker) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	return d.listDirObjects(ctx, dir.GetPath(), args.Refresh)
}

func (d *Chunker) Get(ctx context.Context, pathStr string) (model.Obj, error) {
	if utils.PathEqual(pathStr, "/") {
		return &model.Object{
			Name:     "Root",
			Path:     "/",
			IsFolder: true,
		}, nil
	}
	parent, name := path.Split(utils.FixAndCleanPath(pathStr))
	if parent == "" {
		parent = "/"
	}
	objs, err := d.listDirObjects(ctx, parent, false)
	if err != nil {
		return nil, err
	}
	for _, obj := range objs {
		if obj.GetName() == name {
			return obj, nil
		}
	}
	return nil, errs.ObjectNotFound
}

func (d *Chunker) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	obj := d.linkedObject(file)
	if obj == nil || !obj.Chunked {
		actualPath, err := d.getActualPathForRemote(file.GetPath())
		if err != nil {
			return nil, fmt.Errorf("failed to convert path to remote path: %w", err)
		}
		link, _, err := op.Link(ctx, d.remoteStorage, actualPath, args)
		return link, err
	}

	linkedParts := make([]linkedPart, 0, len(obj.Parts))
	baseClosers := utils.EmptyClosers()
	for _, part := range obj.Parts {
		actualPath, err := d.getActualChunkPath(obj.GetPath(), part.No, part.XactID)
		if err != nil {
			return nil, fmt.Errorf("failed to convert chunk path: %w", err)
		}
		link, _, err := op.Link(ctx, d.remoteStorage, actualPath, args)
		if err != nil {
			return nil, err
		}
		if link.MFile != nil {
			baseClosers.Add(link.MFile)
		}
		if link.RangeReadCloser != nil {
			baseClosers.Add(link.RangeReadCloser)
		}
		linkedParts = append(linkedParts, linkedPart{
			part: part,
			link: link,
		})
	}

	return &model.Link{
		RangeReadCloser: &model.RangeReadCloser{
			RangeReader: func(ctx context.Context, httpRange http_range.Range) (io.ReadCloser, error) {
				return d.openChunkReader(ctx, linkedParts, obj.GetSize(), httpRange)
			},
			Closers: baseClosers,
		},
	}, nil
}

func (d *Chunker) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) error {
	dstDirActualPath, err := d.getActualPathForRemote(parentDir.GetPath())
	if err != nil {
		return fmt.Errorf("failed to convert path to remote path: %w", err)
	}
	return op.MakeDir(ctx, d.remoteStorage, path.Join(dstDirActualPath, dirName))
}

func (d *Chunker) Move(ctx context.Context, srcObj, dstDir model.Obj) error {
	obj := d.linkedObject(srcObj)
	if srcObj.IsDir() || obj == nil || !obj.Chunked {
		srcRemoteActualPath, err := d.getActualPathForRemote(srcObj.GetPath())
		if err != nil {
			return fmt.Errorf("failed to convert path to remote path: %w", err)
		}
		dstRemoteActualPath, err := d.getActualPathForRemote(dstDir.GetPath())
		if err != nil {
			return fmt.Errorf("failed to convert path to remote path: %w", err)
		}
		return op.Move(ctx, d.remoteStorage, srcRemoteActualPath, dstRemoteActualPath)
	}

	dstRemoteActualPath, err := d.getActualPathForRemote(dstDir.GetPath())
	if err != nil {
		return fmt.Errorf("failed to convert path to remote path: %w", err)
	}
	for _, logicalPath := range d.chunkPathsForObject(obj) {
		actualPath, err := d.getActualPathForRemote(logicalPath)
		if err != nil {
			return err
		}
		if err := op.Move(ctx, d.remoteStorage, actualPath, dstRemoteActualPath); err != nil {
			return err
		}
	}
	return nil
}

func (d *Chunker) Rename(ctx context.Context, srcObj model.Obj, newName string) error {
	obj := d.linkedObject(srcObj)
	if srcObj.IsDir() || obj == nil || !obj.Chunked {
		remoteActualPath, err := d.getActualPathForRemote(srcObj.GetPath())
		if err != nil {
			return fmt.Errorf("failed to convert path to remote path: %w", err)
		}
		return op.Rename(ctx, d.remoteStorage, remoteActualPath, newName)
	}

	for _, part := range obj.Parts {
		actualPath, err := d.getActualChunkPath(obj.GetPath(), part.No, part.XactID)
		if err != nil {
			return err
		}
		newChunkName := d.chunkPartBaseName(path.Join(path.Dir(obj.GetPath()), newName), part.No, part.XactID)
		if err := op.Rename(ctx, d.remoteStorage, actualPath, newChunkName); err != nil {
			return err
		}
	}
	if obj.UsesMeta {
		actualPath, err := d.getActualPathForRemote(obj.GetPath())
		if err != nil {
			return err
		}
		if err := op.Rename(ctx, d.remoteStorage, actualPath, newName); err != nil {
			return err
		}
	}
	return nil
}

func (d *Chunker) Copy(ctx context.Context, srcObj, dstDir model.Obj) error {
	obj := d.linkedObject(srcObj)
	if srcObj.IsDir() || obj == nil || !obj.Chunked {
		srcRemoteActualPath, err := d.getActualPathForRemote(srcObj.GetPath())
		if err != nil {
			return fmt.Errorf("failed to convert path to remote path: %w", err)
		}
		dstRemoteActualPath, err := d.getActualPathForRemote(dstDir.GetPath())
		if err != nil {
			return fmt.Errorf("failed to convert path to remote path: %w", err)
		}
		return op.Copy(ctx, d.remoteStorage, srcRemoteActualPath, dstRemoteActualPath)
	}

	dstRemoteActualPath, err := d.getActualPathForRemote(dstDir.GetPath())
	if err != nil {
		return fmt.Errorf("failed to convert path to remote path: %w", err)
	}
	for _, logicalPath := range d.chunkPathsForObject(obj) {
		actualPath, err := d.getActualPathForRemote(logicalPath)
		if err != nil {
			return err
		}
		if err := op.Copy(ctx, d.remoteStorage, actualPath, dstRemoteActualPath); err != nil {
			return err
		}
	}
	return nil
}

func (d *Chunker) Remove(ctx context.Context, obj model.Obj) error {
	chunkedObj := d.linkedObject(obj)
	if obj.IsDir() || chunkedObj == nil || !chunkedObj.Chunked {
		remoteActualPath, err := d.getActualPathForRemote(obj.GetPath())
		if err != nil {
			return fmt.Errorf("failed to convert path to remote path: %w", err)
		}
		return op.Remove(ctx, d.remoteStorage, remoteActualPath)
	}

	for _, logicalPath := range d.chunkPathsForObject(chunkedObj) {
		actualPath, err := d.getActualPathForRemote(logicalPath)
		if err != nil {
			return err
		}
		if err := op.Remove(ctx, d.remoteStorage, actualPath); err != nil {
			return err
		}
	}
	return nil
}

func (d *Chunker) Put(ctx context.Context, dstDir model.Obj, streamer model.FileStreamer, up driver.UpdateProgress) error {
	dstDirActualPath, err := d.getActualPathForRemote(dstDir.GetPath())
	if err != nil {
		return fmt.Errorf("failed to convert path to remote path: %w", err)
	}

	existing := d.linkedObject(streamer.GetExist())
	logicalPath := path.Join(dstDir.GetPath(), streamer.GetName())
	if streamer.GetSize() <= d.ChunkSize {
		if err := op.Put(ctx, d.remoteStorage, dstDirActualPath, streamer, up, false); err != nil {
			return err
		}
		return d.cleanupReplacedObject(ctx, existing, d.buildKeepSet(logicalPath))
	}

	if up == nil {
		up = func(float64) {}
	}

	var (
		md5Hasher  hash.Hash
		sha1Hasher hash.Hash
		writers    []io.Writer
	)
	switch d.HashType {
	case "md5":
		md5Hasher = md5.New()
		writers = append(writers, md5Hasher)
	case "sha1":
		sha1Hasher = sha1.New()
		writers = append(writers, sha1Hasher)
	}
	writers = append(writers, driver.NewProgress(streamer.GetSize(), up))

	baseReader := io.TeeReader(streamer, io.MultiWriter(writers...))
	xactID := strconv.FormatInt(time.Now().UnixNano(), 36)
	if len(xactID) > 9 {
		xactID = xactID[len(xactID)-9:]
	}
	if len(xactID) < 4 {
		xactID = fmt.Sprintf("%04s", xactID)
	}

	chunkCount := 0
	remaining := streamer.GetSize()
	keepPaths := []string{logicalPath}
	for remaining > 0 {
		chunkLen := utils.Min(remaining, d.ChunkSize)
		chunkName := d.chunkPartBaseName(logicalPath, chunkCount, xactIDIfNeeded(d.MetaFormat, xactID))
		chunkPath := d.makeChunkName(logicalPath, chunkCount, xactIDIfNeeded(d.MetaFormat, xactID))
		partReader := driver.NewLimitedUploadStream(ctx, &driver.ReaderWithCtx{
			Reader: io.LimitReader(baseReader, chunkLen),
			Ctx:    ctx,
		})
		partStream := &stream.FileStream{
			Obj: &model.Object{
				Name:     chunkName,
				Size:     chunkLen,
				Modified: streamer.ModTime(),
				Ctime:    streamer.CreateTime(),
				IsFolder: false,
			},
			Reader:            partReader,
			Mimetype:          "application/octet-stream",
			WebPutAsTask:      streamer.NeedStore(),
			ForceStreamUpload: true,
		}
		if err := op.Put(ctx, d.remoteStorage, dstDirActualPath, partStream, nil, false); err != nil {
			return err
		}
		keepPaths = append(keepPaths, chunkPath)
		remaining -= chunkLen
		chunkCount++
	}

	if d.MetaFormat == "simplejson" {
		md5Value := ""
		if md5Hasher != nil {
			md5Value = hex.EncodeToString(md5Hasher.Sum(nil))
		}
		sha1Value := ""
		if sha1Hasher != nil {
			sha1Value = hex.EncodeToString(sha1Hasher.Sum(nil))
		}
		txn := xactID
		metaData, err := marshalMetadata(streamer.GetSize(), chunkCount, md5Value, sha1Value, txn)
		if err != nil {
			return err
		}
		metaStream := &stream.FileStream{
			Obj: &model.Object{
				Name:     streamer.GetName(),
				Size:     int64(len(metaData)),
				Modified: streamer.ModTime(),
				Ctime:    streamer.CreateTime(),
				IsFolder: false,
			},
			Reader:            bytes.NewReader(metaData),
			Mimetype:          "application/json",
			WebPutAsTask:      false,
			ForceStreamUpload: true,
		}
		if err := op.Put(ctx, d.remoteStorage, dstDirActualPath, metaStream, nil, false); err != nil {
			return err
		}
	} else {
		actualPath, err := d.getActualPathForRemote(logicalPath)
		if err == nil {
			_ = op.Remove(ctx, d.remoteStorage, actualPath)
		}
	}

	return d.cleanupReplacedObject(ctx, existing, d.buildKeepSet(keepPaths...))
}

func xactIDIfNeeded(metaFormat, xactID string) string {
	if metaFormat == "simplejson" {
		return xactID
	}
	return ""
}

var _ driver.Driver = (*Chunker)(nil)
