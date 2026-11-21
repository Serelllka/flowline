package model

type Repository struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Path   string `json:"path"`
	Remote string `json:"remote"`
}

type Branch struct {
	Name string `json:"name"`
	SHA  string `json:"sha"`
}

type PullRequest struct {
	Number     int    `json:"number"`
	Title      string `json:"title"`
	URL        string `json:"url"`
	Head       string `json:"head"`
	Base       string `json:"base"`
	HeadSHA    string `json:"headSha"`
	BaseSHA    string `json:"baseSha"`
	Repository string `json:"repository"`
}

type GitContext struct {
	Repository Repository   `json:"repository"`
	Branch     string       `json:"branch"`
	SHA        string       `json:"sha"`
	PR         *PullRequest `json:"pr"`
}
