package chunker

import (
	"context"
	"testing"
	"time"

	"github.com/alist-org/alist/v3/internal/model"
)

func newTestChunker(t *testing.T) *Chunker {
	t.Helper()
	d := &Chunker{
		Addition: Addition{
			NameFormat: defaultChunkNameFmt,
			StartFrom:  defaultStartFrom,
		},
	}
	if err := d.setChunkNameFormat(d.NameFormat); err != nil {
		t.Fatalf("setChunkNameFormat: %v", err)
	}
	return d
}

func TestParseChunkName(t *testing.T) {
	d := newTestChunker(t)

	mainName, chunkNo, ctrlType, xactID := d.parseChunkName("movie.mkv.rclone_chunk.001")
	if mainName != "movie.mkv" || chunkNo != 0 || ctrlType != "" || xactID != "" {
		t.Fatalf("unexpected parse result: main=%q no=%d ctrl=%q txn=%q", mainName, chunkNo, ctrlType, xactID)
	}

	mainName, chunkNo, ctrlType, xactID = d.parseChunkName("movie.mkv.rclone_chunk.003_abcd")
	if mainName != "movie.mkv" || chunkNo != 2 || ctrlType != "" || xactID != "abcd" {
		t.Fatalf("unexpected temp parse result: main=%q no=%d ctrl=%q txn=%q", mainName, chunkNo, ctrlType, xactID)
	}

	mainName, chunkNo, ctrlType, xactID = d.parseChunkName("movie.mkv.rclone_chunk._meta")
	if mainName != "movie.mkv" || chunkNo != -1 || ctrlType != "meta" || xactID != "" {
		t.Fatalf("unexpected control parse result: main=%q no=%d ctrl=%q txn=%q", mainName, chunkNo, ctrlType, xactID)
	}
}

func TestNamedMagicNameFormat(t *testing.T) {
	d := &Chunker{
		Addition: Addition{
			NameFormat: "chunk-{name}-{chunk:4}.bin",
			StartFrom:  defaultStartFrom,
		},
	}
	if err := d.setChunkNameFormat(d.NameFormat); err != nil {
		t.Fatalf("setChunkNameFormat named magic: %v", err)
	}

	got := d.makeChunkName("/video/movie.mkv", 0, "")
	want := "/video/chunk-movie.mkv-0001.bin"
	if got != want {
		t.Fatalf("makeChunkName = %q, want %q", got, want)
	}

	mainName, chunkNo, ctrlType, xactID := d.parseChunkName("chunk-movie.mkv-0003.bin")
	if mainName != "movie.mkv" || chunkNo != 2 || ctrlType != "" || xactID != "" {
		t.Fatalf("unexpected named magic parse result: main=%q no=%d ctrl=%q txn=%q", mainName, chunkNo, ctrlType, xactID)
	}
}

func TestLegacyNameFormatRejected(t *testing.T) {
	d := &Chunker{
		Addition: Addition{
			NameFormat: "chunk-*.###.bin",
			StartFrom:  defaultStartFrom,
		},
	}
	if err := d.setChunkNameFormat(d.NameFormat); err == nil {
		t.Fatal("expected legacy syntax to be rejected")
	}
}

func TestMarshalAndUnmarshalMetadata(t *testing.T) {
	data, err := marshalMetadata(123, 2, "5d41402abc4b2a76b9719d911017c592", "", "")
	if err != nil {
		t.Fatalf("marshalMetadata: %v", err)
	}
	meta, err := unmarshalMetadata(data)
	if err != nil {
		t.Fatalf("unmarshalMetadata: %v", err)
	}
	if meta.Version != 1 || meta.Size != 123 || meta.NChunks != 2 || meta.MD5 == "" || meta.XactID != "" {
		t.Fatalf("unexpected metadata: %+v", meta)
	}

	data, err = marshalMetadata(456, 3, "", "da39a3ee5e6b4b0d3255bfef95601890afd80709", "txn1")
	if err != nil {
		t.Fatalf("marshalMetadata with txn: %v", err)
	}
	meta, err = unmarshalMetadata(data)
	if err != nil {
		t.Fatalf("unmarshalMetadata with txn: %v", err)
	}
	if meta.Version != 2 || meta.Size != 456 || meta.NChunks != 3 || meta.SHA1 == "" || meta.XactID != "txn1" {
		t.Fatalf("unexpected txn metadata: %+v", meta)
	}
}

func TestBuildListedObjectWithoutMetadata(t *testing.T) {
	d := newTestChunker(t)
	now := time.Now()

	obj, ok, err := d.buildListedObject(context.Background(), "/", "archive.bin", &groupInfo{
		partsByXact: map[string]map[int]chunkPart{
			"": {
				0: {No: 0, Size: 5},
				1: {No: 1, Size: 7},
			},
		},
	})
	if err != nil {
		t.Fatalf("buildListedObject: %v", err)
	}
	if !ok {
		t.Fatal("expected grouped object")
	}
	grouped, ok := obj.(*Object)
	if !ok {
		t.Fatalf("expected *Object, got %T", obj)
	}
	if !grouped.Chunked || grouped.GetSize() != 12 || len(grouped.Parts) != 2 {
		t.Fatalf("unexpected grouped object: %+v", grouped)
	}

	raw, ok, err := d.buildListedObject(context.Background(), "/", "raw.txt", &groupInfo{
		base: &locatedObj{
			Obj: &model.Object{
				Name:     "raw.txt",
				Size:     9,
				Modified: now,
				Ctime:    now,
			},
			RemoteIndex: 0,
		},
		partsByXact: map[string]map[int]chunkPart{},
	})
	if err != nil {
		t.Fatalf("build raw listed object: %v", err)
	}
	if !ok {
		t.Fatal("expected raw object")
	}
	rawObj := raw.(*Object)
	if rawObj.Chunked || rawObj.GetSize() != 9 {
		t.Fatalf("unexpected raw object: %+v", rawObj)
	}
}

func TestConfiguredRemotePaths(t *testing.T) {
	d := &Chunker{
		Addition: Addition{
			RemotePath:  "/s1/chunks",
			RemotePaths: "\n/s2/chunks\n/s1/chunks\n /s3/chunks \n",
		},
	}
	got := d.configuredRemotePaths()
	want := []string{"/s1/chunks", "/s2/chunks", "/s3/chunks"}
	if len(got) != len(want) {
		t.Fatalf("configuredRemotePaths length = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("configuredRemotePaths[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestBuildKeepSetSeparatesRemoteTargets(t *testing.T) {
	d := newTestChunker(t)
	keep := d.buildKeepSet(
		objectLocation{LogicalPath: "/movie.bin", RemoteIndex: 0},
		objectLocation{LogicalPath: "/movie.bin", RemoteIndex: 1},
	)
	if len(keep) != 2 {
		t.Fatalf("buildKeepSet should keep distinct entries per remote target, got %d", len(keep))
	}
}

func TestChunkTargetIndexes(t *testing.T) {
	d := newTestChunker(t)
	d.remoteTargets = []remoteTarget{{}, {}, {}}
	d.StoreChunksInPrimary = true
	if got := d.chunkTargetIndexes(); len(got) != 3 || got[0] != 0 || got[1] != 1 || got[2] != 2 {
		t.Fatalf("chunkTargetIndexes with primary = %v", got)
	}
	if got := d.chunkTargetIndex(4); got != 1 {
		t.Fatalf("chunkTargetIndex with primary = %d, want 1", got)
	}

	d.StoreChunksInPrimary = false
	if got := d.chunkTargetIndexes(); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("chunkTargetIndexes without primary = %v", got)
	}
	if got := d.chunkTargetIndex(0); got != 1 {
		t.Fatalf("chunkTargetIndex without primary for first chunk = %d, want 1", got)
	}
	if got := d.chunkTargetIndex(3); got != 2 {
		t.Fatalf("chunkTargetIndex without primary for chunk 3 = %d, want 2", got)
	}

	d.remoteTargets = []remoteTarget{{}}
	if got := d.chunkTargetIndex(0); got != 0 {
		t.Fatalf("single target chunkTargetIndex = %d, want 0", got)
	}
}
