package report

// Report is the rendered v2 report artifact returned to the caller.
type Report struct {
	Status   string `json:"status"`
	Format   string `json:"format"`
	Markdown string `json:"markdown"`
}
