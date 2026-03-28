package streamtape

import (
	"context"
	"crypto/tls"
	"testing"
	"time"

	"github.com/alist-org/alist/v3/drivers/base"
	"github.com/alist-org/alist/v3/internal/model"
	"github.com/go-resty/resty/v2"
)

const (
	testLogin = ""
	testKey   = ""
)

func init() {
	// Initialize RestyClient for testing
	base.RestyClient = resty.New().
		SetHeader("user-agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36").
		SetRetryCount(3).
		SetRetryResetReaders(true).
		SetTimeout(30 * time.Second).
		SetTLSClientConfig(&tls.Config{InsecureSkipVerify: true})
}

func newTestDriver() *Streamtape {
	return &Streamtape{
		Addition: Addition{
			APILogin: testLogin,
			APIKey:   testKey,
		},
	}
}

// TestDriverList tests List method
func TestDriverList(t *testing.T) {
	d := newTestDriver()
	d.RootID.RootFolderID = "0"

	ctx := context.Background()
	objs, err := d.List(ctx, &model.Object{ID: "0", IsFolder: true}, model.ListArgs{})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	t.Logf("List returned %d objects", len(objs))
	for _, obj := range objs {
		t.Logf("  - %s (folder=%v)", obj.GetName(), obj.IsDir())
	}
}

// TestDriverPutURL tests PutURL method - remote upload
func TestDriverPutURL(t *testing.T) {
	d := newTestDriver()
	d.RootID.RootFolderID = "0"

	t.Logf("Driver initialized: APILogin=%s, APIKey=%s, RootFolderID=%s",
		d.APILogin, d.APIKey, d.RootID.RootFolderID)

	ctx := context.Background()
	// Pass a valid root directory object instead of nil
	rootDir := &model.Object{ID: "d:0", IsFolder: true}
	obj, err := d.PutURL(ctx, rootDir, "test.txt", "https://example.com/test.txt")
	if err != nil {
		t.Fatalf("PutURL failed: %v", err)
	}
	t.Logf("PutURL returned: id=%s name=%s", obj.GetID(), obj.GetName())

	// Extract upload ID
	uploadID := remoteUploadIDFromObjID(obj.GetID())
	if uploadID == "" {
		t.Fatal("PutURL returned invalid ID format")
	}
	t.Logf("Upload ID: %s", uploadID)

	// Pass Obj with the upload ID so extractRemoteUploadID can work
	uploadObj := &model.Object{ID: obj.GetID()}

	// Test remotedl_status via Other method
	statusResult, err := d.Other(ctx, model.OtherArgs{
		Obj:    uploadObj,
		Method: "remotedl_status",
		Data:   map[string]interface{}{"id": uploadID},
	})
	if err != nil {
		t.Fatalf("remotedl_status failed: %v", err)
	}
	t.Logf("remotedl_status: %+v", statusResult)

	// Test remotedl_remove via Other method
	removeResult, err := d.Other(ctx, model.OtherArgs{
		Obj:    uploadObj,
		Method: "remotedl_remove",
		Data:   map[string]interface{}{"id": uploadID},
	})
	if err != nil {
		t.Fatalf("remotedl_remove failed: %v", err)
	}
	t.Logf("remotedl_remove: %v", removeResult)
}

// TestDriverFileInfo tests file_info via Other method
func TestDriverFileInfo(t *testing.T) {
	d := newTestDriver()

	// First get a file ID from list
	ctx := context.Background()
	objs, err := d.List(ctx, &model.Object{ID: "0", IsFolder: true}, model.ListArgs{})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	// Find a file in subfolders
	var fileID string
	var fileName string
	for _, obj := range objs {
		if obj.IsDir() {
			subObjs, err := d.List(ctx, obj, model.ListArgs{})
			if err != nil {
				continue
			}
			for _, subObj := range subObjs {
				if !subObj.IsDir() {
					fileID = subObj.GetID()
					fileName = subObj.GetName()
					break
				}
			}
			if fileID != "" {
				break
			}
		}
	}

	if fileID == "" {
		t.Skip("No files found for file_info test")
	}
	t.Logf("Testing file_info with file: %s (id=%s)", fileName, fileID)

	// Test file_info via Other method
	infoResult, err := d.Other(ctx, model.OtherArgs{
		Method: "file_info",
		Obj:    &model.Object{ID: fileID},
	})
	if err != nil {
		t.Fatalf("file_info failed: %v", err)
	}
	t.Logf("file_info: %+v", infoResult)
}

// TestDriverThumbnail tests thumbnail via Other method
func TestDriverThumbnail(t *testing.T) {
	d := newTestDriver()

	// First get a file ID from list
	ctx := context.Background()
	objs, err := d.List(ctx, &model.Object{ID: "0", IsFolder: true}, model.ListArgs{})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	// Find a file in subfolders
	var fileID string
	for _, obj := range objs {
		if obj.IsDir() {
			subObjs, err := d.List(ctx, obj, model.ListArgs{})
			if err != nil {
				continue
			}
			for _, subObj := range subObjs {
				if !subObj.IsDir() {
					fileID = subObj.GetID()
					break
				}
			}
			if fileID != "" {
				break
			}
		}
	}

	if fileID == "" {
		t.Skip("No files found for thumbnail test")
	}
	t.Logf("Testing thumbnail with file id=%s", fileID)

	// Test thumbnail via Other method
	thumbResult, err := d.Other(ctx, model.OtherArgs{
		Method: "thumbnail",
		Obj:    &model.Object{ID: fileID},
	})
	if err != nil {
		t.Fatalf("thumbnail failed: %v", err)
	}
	t.Logf("thumbnail: %v", thumbResult)
}

// TestDriverConversionStatus tests conversion_status via Other method
func TestDriverConversionStatus(t *testing.T) {
	d := newTestDriver()

	ctx := context.Background()

	// Test running conversions
	runningResult, err := d.Other(ctx, model.OtherArgs{
		Method: "conversion_status",
	})
	if err != nil {
		t.Fatalf("conversion_status (running) failed: %v", err)
	}
	t.Logf("conversion_status (running): %+v", runningResult)

	// Test failed conversions
	failedResult, err := d.Other(ctx, model.OtherArgs{
		Method: "conversion_status",
		Data:   map[string]interface{}{"type": "failed"},
	})
	if err != nil {
		t.Fatalf("conversion_status (failed) failed: %v", err)
	}
	t.Logf("conversion_status (failed): %+v", failedResult)
}