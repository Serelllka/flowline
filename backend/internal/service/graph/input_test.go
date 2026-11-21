package graph

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"flowline/internal/model"
	"flowline/internal/testutil"
)

func jsonText(value string) *string { return &value }

func TestExecutionTypesAndJSONValidation(t *testing.T) {
	for _, kind := range []string{"ai", "", "develop", "review", "test", "publish", "prompt"} {
		p := testutil.Pipeline()
		p.Nodes[0].Type = kind
		if problems := ValidateGraph(p); len(problems) != 0 {
			t.Fatalf("legacy/AI type %q rejected: %v", kind, problems)
		}
	}

	for _, tt := range []struct {
		name string
		edit func(*model.Node)
		want string
	}{
		{"unknown", func(n *model.Node) { n.Type = "typo" }, "неизвестный тип"},
		{"empty", func(n *model.Node) { n.InputJSON = jsonText("") }, "JSON"},
		{"array", func(n *model.Node) { n.InputJSON = jsonText("[]") }, "JSON"},
		{"null", func(n *model.Node) { n.InputJSON = jsonText("null") }, "JSON"},
		{"trailing", func(n *model.Node) { n.InputJSON = jsonText("{} {}") }, "лишние данные"},
		{"syntax", func(n *model.Node) { n.InputJSON = jsonText(`{"x":}`) }, "JSON"},
		{"missing", func(n *model.Node) { n.InputJSON = jsonText(`{"x":["{{absent}}"]}`) }, "отсутствующий вход"},
		{"malformed", func(n *model.Node) { n.InputJSON = jsonText(`{"x":"{{branch"}`) }, "незакрытая"},
		{"AI source", func(n *model.Node) { n.Outputs[0].Source = "stdout" }, "источник"},
		{"CMD source", func(n *model.Node) { n.Type, n.Command = "cmd", "cat" }, "источник"},
		{"CMD script", func(n *model.Node) { n.Type = "cmd"; n.Outputs[0].Source = "stdout" }, "пустой скрипт"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := testutil.Pipeline()
			tt.edit(&p.Nodes[1])
			if got := strings.Join(ValidateGraph(p), "; "); !strings.Contains(got, tt.want) {
				t.Fatalf("want %q in %q", tt.want, got)
			}
		})
	}

	p := testutil.Pipeline()
	p.Nodes[1].Type, p.Nodes[1].Command = "cmd", "printf '{{missing}}'"
	p.Nodes[1].Executor, p.Nodes[1].Prompt = "", "{{also_missing}}"
	p.Nodes[1].Outputs[0].Source = "stdout"
	p.Nodes[1].InputJSON = jsonText(`{"literal {{key}}":"value"}`)
	if problems := ValidateGraph(p); len(problems) != 0 {
		t.Fatalf("CMD interpreted code/prompt or JSON keys: %v", problems)
	}
}

func TestJSONSubstitutionPreservesDataAndNumbers(t *testing.T) {
	p := testutil.Pipeline()
	p.Nodes[1].Type = "ai"
	p.Nodes[1].InputJSON = jsonText(`{"nested":[{"value":"prefix {{changes}} / {{branch}}"}],"big":9007199254740993,"literal {{changes}}":true}`)
	value := "quotes \" and newline\n$(touch injected); {{absent}}"
	runs := map[string]*model.NodeRun{"dev": {Status: "success", Outputs: map[string]string{"changes": value}}}
	gitContext := model.GitContext{Branch: "main"}
	inputs, err := ResolveInputs(p, p.Nodes[1], gitContext, runs)
	if err != nil {
		t.Fatal(err)
	}

	resolved, err := ResolveInputJSON(p, p.Nodes[1], gitContext, runs, inputs)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Context      model.GitContext
		Predecessors map[string]map[string]string
		Data         struct {
			Nested []struct{ Value string }
			Big    json.Number
		}
	}
	if err := json.Unmarshal(resolved, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Nested[0].Value != "prefix "+value+" / main" || envelope.Data.Big.String() != "9007199254740993" || envelope.Context.Branch != "main" || envelope.Predecessors["dev"]["changes"] != value {
		t.Fatalf("data changed: %s", resolved)
	}
}

func TestJSONRenameKeepsStreamsAliasesAndLiteralScripts(t *testing.T) {
	p := testutil.Pipeline()
	p.Nodes[0].Type, p.Nodes[0].Command = "cmd", "cat"
	p.Nodes[0].Outputs[0].Name = "stdout"
	p.Nodes[1].Type, p.Nodes[1].Command = "cmd", "printf '{{stdout}}'"
	p.Nodes[1].Outputs[0].Source = "stdout"
	p.Nodes[1].InputJSON = jsonText(`{"{{stdout}}":["{{ stdout }}","{{dev.stdout}}","{{alias}}"]}`)
	p.Nodes[1].Inputs = map[string]model.Source{"alias": {NodeID: "dev", OutputID: "changes-id"}}
	originalScript := p.Nodes[1].Command

	if err := RenameOutput(&p, "dev", "changes-id", "text"); err != nil {
		t.Fatal(err)
	}

	if p.Nodes[0].Outputs[0] != (model.Output{ID: "changes-id", Name: "text", Source: "stdout"}) || p.Nodes[1].Command != originalScript {
		t.Fatal("rename changed identity or script")
	}
	var data map[string][]string
	if err := json.Unmarshal([]byte(*p.Nodes[1].InputJSON), &data); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(data["{{stdout}}"], []string{"{{ text }}", "{{dev.text}}", "{{alias}}"}) {
		t.Fatalf("unexpected rewrite: %v", data)
	}
	if problems := ValidateGraph(p); len(problems) != 0 {
		t.Fatal(problems)
	}
}

func TestJSONAmbiguityAndRenameCollision(t *testing.T) {
	p := testutil.Pipeline()
	p.Nodes[1].Prompt = "Review"
	p.Nodes[1].InputJSON = jsonText(`{"text":"{{changes}} / {{diff}}"}`)
	other := model.Node{ID: "other", Name: "Other", Executor: "test", Prompt: "Build", Outputs: []model.Output{{ID: "other-out", Name: "diff"}}}
	p.Nodes = append(p.Nodes, other)
	p.Edges = append(p.Edges, model.Edge{From: "other", To: "review"})

	if err := RenameOutput(&p, "dev", "changes-id", "diff"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(*p.Nodes[1].InputJSON, "{{dev.diff}} / {{other.diff}}") {
		t.Fatal(*p.Nodes[1].InputJSON)
	}

	p.Nodes[1].InputJSON = jsonText(`{"text":"{{diff}}"}`)
	if problems := strings.Join(ValidateGraph(p), ";"); !strings.Contains(problems, "неоднозначная") {
		t.Fatal(problems)
	}
	before, _ := json.Marshal(p)
	if err := RenameOutput(&p, "dev", "changes-id", "renamed"); err == nil {
		t.Fatal("ambiguous JSON rename accepted")
	}
	after, _ := json.Marshal(p)
	if string(after) != string(before) {
		t.Fatal("failed rename mutated pipeline")
	}
}
