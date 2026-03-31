package yunpan360

import "testing"

func TestOwnerQIDFromToken(t *testing.T) {
	token := "3061246061.9.cb696851.679627082.16700351802400171.1774866942"
	if got := ownerQIDFromToken(token); got != "679627082" {
		t.Fatalf("ownerQIDFromToken() = %q, want %q", got, "679627082")
	}
}

func TestParseCookieDownloadSessionFromText(t *testing.T) {
	page := `window.__NUXT__={"token":"3061246061.9.cb696851.679627082.16700351802400171.1774866942","owner_qid":"679627082"}`
	session := parseCookieDownloadSessionFromText(page)
	if session == nil {
		t.Fatal("parseCookieDownloadSessionFromText() = nil")
	}
	if session.OwnerQID != "679627082" {
		t.Fatalf("OwnerQID = %q, want %q", session.OwnerQID, "679627082")
	}
	if session.Token == "" {
		t.Fatal("Token should not be empty")
	}
}

func TestCookieListRespObjectsCarrySession(t *testing.T) {
	resp := CookieListResp{
		Token:    "3061246061.9.cb696851.679627082.16700351802400171.1774866942",
		OwnerQid: "679627082",
		Data: []ListItem{{
			NID:      "17748755101917705",
			FileName: "统计4.mp4",
			FilePath: "/统计4.mp4",
			FileSize: "1024",
		}},
	}

	objs := resp.Objects("/")
	if len(objs) != 1 {
		t.Fatalf("len(objs) = %d, want 1", len(objs))
	}

	obj, ok := objs[0].(*YunpanObject)
	if !ok {
		t.Fatalf("object type = %T, want *YunpanObject", objs[0])
	}
	if obj.OwnerQID != "679627082" {
		t.Fatalf("OwnerQID = %q, want %q", obj.OwnerQID, "679627082")
	}
	if obj.DownloadToken == "" {
		t.Fatal("DownloadToken should not be empty")
	}
}

func TestResolveCookieOwnerQIDFromObject(t *testing.T) {
	d := &Yunpan360{}
	obj := &YunpanObject{
		OwnerQID: "679627082",
	}
	got, err := d.resolveCookieOwnerQID(t.Context(), obj, false)
	if err != nil {
		t.Fatalf("resolveCookieOwnerQID() error = %v", err)
	}
	if got != "679627082" {
		t.Fatalf("resolveCookieOwnerQID() = %q, want %q", got, "679627082")
	}
}

func TestCheckCookieAsyncTaskSuccess(t *testing.T) {
	done, err := checkCookieAsyncTask(CookieAsyncTask{
		Status: 10,
		Action: "File.move",
		Errno:  0,
	}, nil)
	if !done {
		t.Fatal("checkCookieAsyncTask() should be done")
	}
	if err != nil {
		t.Fatalf("checkCookieAsyncTask() error = %v", err)
	}
}

func TestCheckCookieAsyncTaskPending(t *testing.T) {
	done, err := checkCookieAsyncTask(CookieAsyncTask{
		Status: 1,
		Action: "File.move",
	}, nil)
	if done {
		t.Fatal("checkCookieAsyncTask() should be pending")
	}
	if err != nil {
		t.Fatalf("checkCookieAsyncTask() error = %v", err)
	}
}

func TestCheckCookieAsyncTaskIgnoredErrno(t *testing.T) {
	done, err := checkCookieAsyncTask(CookieAsyncTask{
		Status: 10,
		Action: "File.recycle",
		Errno:  3008,
		Errstr: "文件（夹）已移动或删除！",
	}, map[int]struct{}{3008: {}})
	if !done {
		t.Fatal("checkCookieAsyncTask() should be done")
	}
	if err != nil {
		t.Fatalf("checkCookieAsyncTask() error = %v", err)
	}
}
