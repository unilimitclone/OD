package chunker

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/alist-org/alist/v3/internal/fs"
	"github.com/alist-org/alist/v3/internal/model"
	"github.com/alist-org/alist/v3/internal/op"
	"github.com/alist-org/alist/v3/internal/stream"
	"github.com/alist-org/alist/v3/pkg/http_range"
	"github.com/alist-org/alist/v3/pkg/utils"
)

func (d *Chunker) validateOptions() error {
	if d.RemotePath == "" {
		return errors.New("remote_path is required")
	}
	if d.ChunkSize <= 0 {
		return errors.New("chunk_size must be positive")
	}
	switch d.MetaFormat {
	case "simplejson", "none":
	default:
		return fmt.Errorf("unsupported meta_format: %s", d.MetaFormat)
	}
	switch d.HashType {
	case "none", "md5", "sha1":
	default:
		return fmt.Errorf("unsupported hash_type: %s", d.HashType)
	}
	if d.MetaFormat == "none" && d.HashType != "none" {
		return fmt.Errorf("hash_type %q requires meta_format=simplejson", d.HashType)
	}
	return nil
}

func (d *Chunker) setChunkNameFormat(pattern string) error {
	if strings.Count(pattern, "*") != 1 {
		return errors.New("pattern must have exactly one asterisk (*)")
	}
	hashCount := strings.Count(pattern, "#")
	if hashCount < 1 {
		return errors.New("pattern must contain a hash character (#)")
	}
	if strings.Index(pattern, "*") > strings.Index(pattern, "#") {
		return errors.New("asterisk (*) in pattern must come before hashes (#)")
	}
	if ok, _ := regexp.MatchString("^[^#]*[#]+[^#]*$", pattern); !ok {
		return errors.New("hashes (#) in pattern must be consecutive")
	}
	if dir, _ := path.Split(pattern); dir != "" {
		return errors.New("directory separator prohibited")
	}
	if pattern[0] != '*' {
		return errors.New("pattern must start with asterisk")
	}

	reHashes := regexp.MustCompile("[#]+")
	reDigits := "[0-9]+"
	if hashCount > 1 {
		reDigits = fmt.Sprintf("[0-9]{%d,}", hashCount)
	}
	reDataOrCtrl := fmt.Sprintf("(?:(%s)|_(%s))", reDigits, ctrlTypeRegStr)

	strRegex := regexp.QuoteMeta(pattern)
	strRegex = reHashes.ReplaceAllLiteralString(strRegex, reDataOrCtrl)
	strRegex = strings.Replace(strRegex, "\\*", "(.+?)", 1)
	strRegex = fmt.Sprintf("^%s(?:%s|%s)?$", strRegex, tempSuffixRegStr, tempSuffixRegOld)
	d.nameRegexp = regexp.MustCompile(strRegex)

	fmtDigits := "%d"
	if hashCount > 1 {
		fmtDigits = fmt.Sprintf("%%0%dd", hashCount)
	}
	strFmt := strings.ReplaceAll(pattern, "%", "%%")
	strFmt = strings.Replace(strFmt, "*", "%s", 1)
	d.dataNameFmt = reHashes.ReplaceAllLiteralString(strFmt, fmtDigits)
	return nil
}

func (d *Chunker) makeChunkName(filePath string, chunkNo int, xactID string) string {
	dir, baseName := path.Split(filePath)
	name := fmt.Sprintf(d.dataNameFmt, baseName, chunkNo+d.StartFrom)
	if xactID != "" {
		name += fmt.Sprintf(tempSuffixFormat, xactID)
	}
	return dir + name
}

func (d *Chunker) parseChunkName(filePath string) (parentPath string, chunkNo int, ctrlType, xactID string) {
	dir, name := path.Split(filePath)
	match := d.nameRegexp.FindStringSubmatch(name)
	if match == nil || match[1] == "" {
		return "", -1, "", ""
	}

	chunkNo = -1
	if match[2] != "" {
		n, err := strconv.Atoi(match[2])
		if err != nil {
			return "", -1, "", ""
		}
		chunkNo = n - d.StartFrom
		if chunkNo < 0 {
			return "", -1, "", ""
		}
	}

	if match[4] != "" {
		xactID = match[4]
	}
	if match[5] != "" {
		oldNum, err := strconv.ParseInt(match[5], 10, 64)
		if err != nil || oldNum < 0 {
			return "", -1, "", ""
		}
		xactID = fmt.Sprintf(tempSuffixFormat, strconv.FormatInt(oldNum, 36))[1:]
	}

	return dir + match[1], chunkNo, match[3], xactID
}

func marshalMetadata(size int64, nChunks int, md5Value, sha1Value, xactID string) ([]byte, error) {
	version := chunkerMetadataVerion
	if xactID == "" && version == 2 {
		version = 1
	}
	meta := metadataJSON{
		Version:  &version,
		Size:     &size,
		ChunkNum: &nChunks,
		MD5:      md5Value,
		SHA1:     sha1Value,
		XactID:   xactID,
	}
	data, err := json.Marshal(&meta)
	if err == nil && len(data) >= maxMetadataSizeWrite {
		return nil, errors.New("metadata can't be this big")
	}
	return data, err
}

func unmarshalMetadata(data []byte) (*chunkMetadata, error) {
	if len(data) > maxMetadataSizeWrite {
		return nil, errors.New("metadata is too large")
	}
	if data == nil || len(data) < 2 || data[0] != '{' || data[len(data)-1] != '}' {
		return nil, errors.New("invalid json")
	}
	var meta metadataJSON
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, err
	}
	if meta.Version == nil || meta.Size == nil || meta.ChunkNum == nil {
		return nil, errors.New("missing required field")
	}
	if *meta.Version < 1 {
		return nil, errors.New("wrong version")
	}
	if *meta.Size < 0 {
		return nil, errors.New("negative file size")
	}
	if *meta.ChunkNum < 1 || *meta.ChunkNum > maxSafeChunkNumber {
		return nil, errors.New("wrong number of chunks")
	}
	if meta.MD5 != "" {
		if _, err := hex.DecodeString(meta.MD5); err != nil || len(meta.MD5) != 32 {
			return nil, errors.New("wrong md5 hash")
		}
	}
	if meta.SHA1 != "" {
		if _, err := hex.DecodeString(meta.SHA1); err != nil || len(meta.SHA1) != 40 {
			return nil, errors.New("wrong sha1 hash")
		}
	}
	if *meta.Version > chunkerMetadataVerion {
		return nil, errors.New("unknown metadata version")
	}
	return &chunkMetadata{
		Version: *meta.Version,
		Size:    *meta.Size,
		NChunks: *meta.ChunkNum,
		MD5:     meta.MD5,
		SHA1:    meta.SHA1,
		XactID:  meta.XactID,
	}, nil
}

func (d *Chunker) joinRemotePath(logicalPath string) string {
	logicalPath = utils.FixAndCleanPath(logicalPath)
	if utils.PathEqual(logicalPath, "/") {
		return d.RemotePath
	}
	return path.Join(d.RemotePath, logicalPath)
}

func (d *Chunker) getActualPathForRemote(logicalPath string) (string, error) {
	_, actualPath, err := op.GetStorageAndActualPath(d.joinRemotePath(logicalPath))
	return actualPath, err
}

func (d *Chunker) getActualChunkPath(filePath string, chunkNo int, xactID string) (string, error) {
	return d.getActualPathForRemote(d.makeChunkName(filePath, chunkNo, xactID))
}

func (d *Chunker) listDirObjects(ctx context.Context, dirPath string, refresh bool) ([]model.Obj, error) {
	remotePath := d.joinRemotePath(dirPath)
	entries, err := fsList(ctx, remotePath, refresh)
	if err != nil {
		return nil, err
	}

	groups := map[string]*groupInfo{}
	var dirs []model.Obj

	for _, entry := range entries {
		if entry.IsDir() {
			dirs = append(dirs, &model.Object{
				Name:     entry.GetName(),
				Path:     path.Join(dirPath, entry.GetName()),
				Size:     0,
				Modified: entry.ModTime(),
				Ctime:    entry.CreateTime(),
				IsFolder: true,
				HashInfo: entry.GetHash(),
			})
			continue
		}

		mainName, chunkNo, ctrlType, xactID := d.parseChunkName(entry.GetName())
		if mainName == "" {
			g := groups[entry.GetName()]
			if g == nil {
				g = &groupInfo{partsByXact: map[string]map[int]chunkPart{}}
				groups[entry.GetName()] = g
			}
			g.base = entry
			continue
		}
		if chunkNo < 0 || ctrlType != "" {
			continue
		}
		g := groups[mainName]
		if g == nil {
			g = &groupInfo{partsByXact: map[string]map[int]chunkPart{}}
			groups[mainName] = g
		}
		if g.partsByXact[xactID] == nil {
			g.partsByXact[xactID] = map[int]chunkPart{}
		}
		g.partsByXact[xactID][chunkNo] = chunkPart{
			No:     chunkNo,
			Size:   entry.GetSize(),
			XactID: xactID,
		}
	}

	result := make([]model.Obj, 0, len(dirs)+len(groups))
	result = append(result, dirs...)
	for name, group := range groups {
		obj, ok, err := d.buildListedObject(ctx, dirPath, name, group)
		if err != nil {
			return nil, err
		}
		if ok {
			result = append(result, obj)
		}
	}
	return result, nil
}

func (d *Chunker) buildListedObject(ctx context.Context, dirPath, name string, group *groupInfo) (model.Obj, bool, error) {
	var meta *chunkMetadata
	var err error
	if group.base != nil && group.base.GetSize() <= maxMetadataSizeRead && len(group.partsByXact) > 0 {
		meta, err = d.readMetadata(ctx, path.Join(dirPath, name), group.base.GetSize())
		if err != nil {
			meta = nil
		}
	}

	if meta == nil && group.base != nil && len(group.partsByXact) == 0 {
		return &Object{
			Object: model.Object{
				Name:     name,
				Path:     path.Join(dirPath, name),
				Size:     group.base.GetSize(),
				Modified: group.base.ModTime(),
				Ctime:    group.base.CreateTime(),
				IsFolder: false,
				HashInfo: group.base.GetHash(),
			},
			Main: group.base,
		}, true, nil
	}

	selected := map[int]chunkPart{}
	switch {
	case meta != nil:
		selected = group.partsByXact[meta.XactID]
	case group.base == nil:
		selected = group.partsByXact[""]
	default:
		return &Object{
			Object: model.Object{
				Name:     name,
				Path:     path.Join(dirPath, name),
				Size:     group.base.GetSize(),
				Modified: group.base.ModTime(),
				Ctime:    group.base.CreateTime(),
				IsFolder: false,
				HashInfo: group.base.GetHash(),
			},
			Main: group.base,
		}, true, nil
	}

	parts := sortChunkParts(selected)
	if len(parts) == 0 {
		if meta != nil {
			return &Object{
				Object: model.Object{
					Name:     name,
					Path:     path.Join(dirPath, name),
					Size:     meta.Size,
					Modified: group.base.ModTime(),
					Ctime:    group.base.CreateTime(),
					IsFolder: false,
					HashInfo: buildHashInfo(meta),
				},
				Main:     group.base,
				Meta:     meta,
				Chunked:  true,
				UsesMeta: true,
			}, true, nil
		}
		return nil, false, nil
	}

	size := int64(0)
	for _, part := range parts {
		size += part.Size
	}
	modified := time.Time{}
	ctime := time.Time{}
	if group.base != nil {
		modified = group.base.ModTime()
		ctime = group.base.CreateTime()
	}
	if meta != nil {
		size = meta.Size
	}

	return &Object{
		Object: model.Object{
			Name:     name,
			Path:     path.Join(dirPath, name),
			Size:     size,
			Modified: modified,
			Ctime:    ctime,
			IsFolder: false,
			HashInfo: buildHashInfo(meta),
		},
		Main:     group.base,
		Parts:    parts,
		Meta:     meta,
		Chunked:  true,
		UsesMeta: meta != nil,
	}, true, nil
}

func (d *Chunker) readMetadata(ctx context.Context, logicalPath string, size int64) (*chunkMetadata, error) {
	actualPath, err := d.getActualPathForRemote(logicalPath)
	if err != nil {
		return nil, err
	}
	link, obj, err := op.Link(ctx, d.remoteStorage, actualPath, model.LinkArgs{})
	if err != nil {
		return nil, err
	}
	ss, err := stream.NewSeekableStream(stream.FileStream{Ctx: ctx, Obj: obj}, link)
	if err != nil {
		return nil, err
	}
	defer ss.Close()

	reader, err := ss.RangeRead(http_range.Range{Start: 0, Length: size})
	if err != nil {
		return nil, err
	}
	if closer, ok := reader.(io.Closer); ok {
		defer closer.Close()
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	return unmarshalMetadata(data)
}

func sortChunkParts(parts map[int]chunkPart) []chunkPart {
	result := make([]chunkPart, 0, len(parts))
	for _, part := range parts {
		result = append(result, part)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].No < result[j].No
	})
	return result
}

func buildHashInfo(meta *chunkMetadata) utils.HashInfo {
	if meta == nil {
		return utils.HashInfo{}
	}
	hashes := map[*utils.HashType]string{}
	if meta.MD5 != "" {
		hashes[utils.MD5] = meta.MD5
	}
	if meta.SHA1 != "" {
		hashes[utils.SHA1] = meta.SHA1
	}
	return utils.NewHashInfoByMap(hashes)
}

func (d *Chunker) linkedObject(obj model.Obj) *Object {
	if linked, ok := obj.(*Object); ok {
		return linked
	}
	return nil
}

func (d *Chunker) chunkPathsForObject(obj *Object) []string {
	if obj == nil {
		return nil
	}
	paths := make([]string, 0, len(obj.Parts)+1)
	if obj.Chunked && obj.UsesMeta {
		paths = append(paths, obj.GetPath())
	}
	if !obj.Chunked {
		paths = append(paths, obj.GetPath())
		return paths
	}
	for _, part := range obj.Parts {
		paths = append(paths, d.makeChunkName(obj.GetPath(), part.No, part.XactID))
	}
	return paths
}

func (d *Chunker) cleanupReplacedObject(ctx context.Context, obj *Object, keep map[string]struct{}) error {
	if obj == nil {
		return nil
	}
	var errs []error
	for _, logicalPath := range d.chunkPathsForObject(obj) {
		if _, ok := keep[logicalPath]; ok {
			continue
		}
		actualPath, err := d.getActualPathForRemote(logicalPath)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if err := op.Remove(ctx, d.remoteStorage, actualPath); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (d *Chunker) buildKeepSet(paths ...string) map[string]struct{} {
	keep := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		if p == "" {
			continue
		}
		keep[utils.FixAndCleanPath(p)] = struct{}{}
	}
	return keep
}

func (d *Chunker) chunkPartBaseName(filePath string, chunkNo int, xactID string) string {
	return path.Base(d.makeChunkName(filePath, chunkNo, xactID))
}

func (d *Chunker) openChunkReader(ctx context.Context, parts []linkedPart, totalSize int64, req http_range.Range) (io.ReadCloser, error) {
	if req.Start < 0 || req.Start > totalSize {
		return nil, fmt.Errorf("range start out of bound")
	}
	if req.Length < 0 || req.Start+req.Length > totalSize {
		req.Length = totalSize - req.Start
	}
	if req.Length == 0 {
		return io.NopCloser(strings.NewReader("")), nil
	}

	var (
		readers   []io.Reader
		closers   = utils.EmptyClosers()
		offset    int64
		remaining = req.Length
	)
	for _, part := range parts {
		partStart := offset
		partEnd := offset + part.part.Size
		offset = partEnd
		if req.Start >= partEnd || remaining <= 0 {
			continue
		}
		localStart := int64(0)
		if req.Start > partStart {
			localStart = req.Start - partStart
		}
		localLength := utils.Min(part.part.Size-localStart, remaining)
		rc, err := d.openPartRange(ctx, part.link, part.part.Size, localStart, localLength)
		if err != nil {
			_ = closers.Close()
			return nil, err
		}
		readers = append(readers, rc)
		closers.Add(rc)
		remaining -= localLength
	}
	if remaining > 0 {
		_ = closers.Close()
		return nil, errors.New("missing chunk data")
	}
	return utils.NewReadCloser(io.MultiReader(readers...), func() error {
		return closers.Close()
	}), nil
}

func (d *Chunker) openPartRange(ctx context.Context, link *model.Link, size, offset, length int64) (io.ReadCloser, error) {
	httpRange := http_range.Range{Start: offset, Length: length}
	switch {
	case link.MFile != nil:
		return io.NopCloser(io.NewSectionReader(link.MFile, offset, length)), nil
	case link.RangeReadCloser != nil:
		return link.RangeReadCloser.RangeRead(ctx, httpRange)
	case link.URL != "":
		rrc, err := stream.GetRangeReadCloserFromLink(size, link)
		if err != nil {
			return nil, err
		}
		rc, err := rrc.RangeRead(ctx, httpRange)
		if err != nil {
			_ = rrc.Close()
			return nil, err
		}
		return utils.NewReadCloser(rc, func() error {
			return rrc.Close()
		}), nil
	default:
		return nil, errors.New("chunk part has no readable link")
	}
}

func fsList(ctx context.Context, remotePath string, refresh bool) ([]model.Obj, error) {
	return fs.List(ctx, remotePath, &fs.ListArgs{NoLog: true, Refresh: refresh})
}
