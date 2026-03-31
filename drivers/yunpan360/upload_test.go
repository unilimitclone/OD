package yunpan360

import (
	"bytes"
	"testing"
	"time"

	"github.com/alist-org/alist/v3/internal/model"
)

func TestBuildUploadPlanComputesFileSHA1AndMD5(t *testing.T) {
	d := &Yunpan360{}
	data := []byte("hello yunpan")
	file := model.NewNopMFile(bytes.NewReader(data))
	plan, err := d.buildUploadPlan(t.Context(), file, "/hello.txt", int64(len(data)), time.Unix(1700000000, 0), nil)
	if err != nil {
		t.Fatalf("buildUploadPlan() error = %v", err)
	}
	if plan.FileSHA1 != "254ec33af17332a3964145f8c6a3dc12833c7ea2" {
		t.Fatalf("FileSHA1 = %q", plan.FileSHA1)
	}
	if plan.FileSum != "fefeffa5b6ae9f39851050b44cacfcb1" {
		t.Fatalf("FileSum = %q", plan.FileSum)
	}
}
