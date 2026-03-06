package streamtape

import "encoding/json"

type apiResponse struct {
	Status int             `json:"status"`
	Msg    string          `json:"msg"`
	Result json.RawMessage `json:"result"`
}

type accountInfo struct {
	APIID    string `json:"apiid"`
	Email    string `json:"email"`
	SignupAt string `json:"signup_at"`
}

type listFolderResult struct {
	Folders []folderItem `json:"folders"`
	Files   []fileItem   `json:"files"`
}

type folderItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type fileItem struct {
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	Link      string `json:"link"`
	CreatedAt int64  `json:"created_at"`
	Downloads int64  `json:"downloads"`
	LinkID    string `json:"linkid"`
	Convert   string `json:"convert"`
}

type dlTicketResult struct {
	Ticket   string `json:"ticket"`
	WaitTime int    `json:"wait_time"`
}

type dlResult struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	URL  string `json:"url"`
}

type createFolderResult struct {
	FolderID string `json:"folderid"`
}

type uploadURLResult struct {
	URL string `json:"url"`
}
