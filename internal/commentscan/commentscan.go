package commentscan

import (
	"go/parser"
	"go/token"
	"strings"
)

type Comment struct {
	Start int    `json:"start"`
	End   int    `json:"end"`
	Text  string `json:"text"`
}

func (c Comment) Lines() int { return c.End - c.Start + 1 }

var directivePrefixes = []string{
	"//go:",
	"//nolint",
	"//line ",
	"//export ",
	"// +build",
	"// Code generated ",
	"// Output:",
	"// Unordered output:",
}

func IsDirective(text string) bool {
	for _, prefix := range directivePrefixes {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}
	return false
}

func ScanGo(filename string, src []byte) ([]Comment, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	var comments []Comment
	for _, group := range file.Comments {
		for _, c := range group.List {
			if IsDirective(c.Text) {
				continue
			}
			comments = append(comments, Comment{
				Start: fset.Position(c.Pos()).Line,
				End:   fset.Position(c.End()).Line,
				Text:  c.Text,
			})
		}
	}
	return comments, nil
}

func Lines(comments []Comment) int {
	total := 0
	for _, c := range comments {
		total += c.Lines()
	}
	return total
}
