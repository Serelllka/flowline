package graph

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"

	"flowline/internal/model"
)

var tokenPattern = regexp.MustCompile(`\{\{([^{}]*?)\}\}`)

// ValidateGraph checks structure and references without invoking an executor.
func ValidateGraph(p model.Pipeline) []string {
	problems := []string{}
	if len(p.Nodes) == 0 {
		problems = append(problems, "Добавьте хотя бы один блок")
	}

	nodes := make(map[string]model.Node, len(p.Nodes))
	for _, n := range p.Nodes {
		if !model.ValidID(n.ID) {
			problems = append(problems, fmt.Sprintf("Некорректный ID блока %q", n.ID))
		}
		if _, exists := nodes[n.ID]; exists {
			problems = append(problems, fmt.Sprintf("Повторяющийся ID блока %q", n.ID))
		}
		nodes[n.ID] = n

		if strings.TrimSpace(n.Name) == "" {
			problems = append(problems, fmt.Sprintf("У блока %q отсутствует название", n.ID))
		}
		if n.ExecutionType() == "" {
			problems = append(problems, fmt.Sprintf("Блок %q: неизвестный тип %q", n.ID, n.Type))
		}
		if n.ExecutionType() == "cmd" && strings.TrimSpace(n.Command) == "" {
			problems = append(problems, fmt.Sprintf("У блока %q пустой скрипт", n.ID))
		}
		if n.ExecutionType() == "ai" && strings.TrimSpace(n.Executor) == "" {
			problems = append(problems, fmt.Sprintf("У блока %q не выбран исполнитель", n.ID))
		}
		if n.ExecutionType() == "ai" && strings.TrimSpace(n.Prompt) == "" {
			problems = append(problems, fmt.Sprintf("У блока %q пустой промпт", n.ID))
		}
		if math.IsNaN(n.X) || math.IsInf(n.X, 0) || math.IsNaN(n.Y) || math.IsInf(n.Y, 0) {
			problems = append(problems, fmt.Sprintf("Некорректные координаты блока %q", n.ID))
		}
		if len(n.Outputs) == 0 {
			problems = append(problems, fmt.Sprintf("У блока %q отсутствуют результаты", n.ID))
		}

		ids, names := map[string]bool{}, map[string]bool{}
		for _, output := range n.Outputs {
			if !n.ValidOutputSource(output) {
				problems = append(problems, fmt.Sprintf("Блок %q: недопустимый источник результата %q для типа %q", n.ID, output.Name, n.Type))
			}

			if !model.ValidID(output.ID) || ids[output.ID] {
				problems = append(problems, fmt.Sprintf("У блока %q некорректный или повторяющийся ID результата %q", n.ID, output.ID))
			}
			if !model.ValidIdentifier(output.Name) || model.IsBuiltinToken(output.Name) || names[output.Name] {
				problems = append(problems, fmt.Sprintf("У блока %q некорректное, зарезервированное или повторяющееся имя результата %q", n.ID, output.Name))
			}
			ids[output.ID], names[output.Name] = true, true
		}
	}

	indegree := make(map[string]int, len(nodes))
	outgoing := make(map[string][]string, len(nodes))
	seenEdges := map[model.Edge]bool{}
	for _, e := range p.Edges {
		if _, ok := nodes[e.From]; !ok {
			problems = append(problems, fmt.Sprintf("Связь ссылается на отсутствующий блок %q", e.From))
			continue
		}
		if _, ok := nodes[e.To]; !ok {
			problems = append(problems, fmt.Sprintf("Связь ссылается на отсутствующий блок %q", e.To))
			continue
		}
		if seenEdges[e] {
			problems = append(problems, fmt.Sprintf("Повторяющаяся связь %q → %q", e.From, e.To))
			continue
		}
		seenEdges[e] = true
		indegree[e.To]++
		outgoing[e.From] = append(outgoing[e.From], e.To)
	}

	ready := []string{}
	for id := range nodes {
		if indegree[id] == 0 {
			ready = append(ready, id)
		}
	}
	visited := 0
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		visited++
		for _, next := range outgoing[id] {
			indegree[next]--
			if indegree[next] == 0 {
				ready = append(ready, next)
			}
		}
	}
	if visited != len(nodes) {
		problems = append(problems, "Граф содержит цикл")
	}

	for _, n := range p.Nodes {
		refs, ambiguous, refErrors := inputReferences(p, n)
		problems = append(problems, refErrors...)
		tokens, err := nodeTokens(n)
		if err != nil {
			problems = append(problems, fmt.Sprintf("Блок %q: %v", n.ID, err))
			continue
		}
		for _, token := range tokens {
			if model.IsBuiltinToken(token) {
				continue
			}
			if ambiguous[token] {
				problems = append(problems, fmt.Sprintf("Блок %q: неоднозначная ссылка {{%s}}; задайте вход или укажите nodeId.outputName", n.ID, token))
				continue
			}
			if _, ok := refs[token]; !ok {
				problems = append(problems, fmt.Sprintf("Блок %q: отсутствующий вход {{%s}}", n.ID, token))
			}
		}
	}

	return problems
}

func inputReferences(p model.Pipeline, n model.Node) (map[string]model.Source, map[string]bool, []string) {
	refs := map[string]model.Source{}
	ambiguous := map[string]bool{}
	problems := []string{}
	predecessors := map[string]model.Node{}
	for _, e := range p.Edges {
		if e.To != n.ID {
			continue
		}
		for _, source := range p.Nodes {
			if source.ID == e.From {
				predecessors[source.ID] = source
			}
		}
	}

	for _, predecessor := range predecessors {
		for _, output := range predecessor.Outputs {
			source := model.Source{NodeID: predecessor.ID, OutputID: output.ID}
			refs[predecessor.ID+"."+output.Name] = source
			if previous, exists := refs[output.Name]; exists && previous != source {
				ambiguous[output.Name] = true
			}
			refs[output.Name] = source
		}
	}

	aliases := make([]string, 0, len(n.Inputs))
	for alias := range n.Inputs {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	for _, alias := range aliases {
		source := n.Inputs[alias]
		if !model.ValidIdentifier(alias) || model.IsBuiltinToken(alias) {
			problems = append(problems, fmt.Sprintf("Блок %q: некорректное или зарезервированное имя входа %q", n.ID, alias))
			continue
		}

		predecessor, exists := predecessors[source.NodeID]
		if !exists {
			problems = append(problems, fmt.Sprintf("Блок %q: вход %q требует прямую связь с блоком %q", n.ID, alias, source.NodeID))
			continue
		}
		if _, exists := outputByID(predecessor, source.OutputID); !exists {
			problems = append(problems, fmt.Sprintf("Блок %q: вход %q ссылается на отсутствующий результат %q", n.ID, alias, source.OutputID))
			continue
		}
		refs[alias] = source
		delete(ambiguous, alias)
	}

	return refs, ambiguous, problems
}

func outputByID(n model.Node, id string) (model.Output, bool) {
	for _, output := range n.Outputs {
		if output.ID == id {
			return output, true
		}
	}

	return model.Output{}, false
}

func promptTokens(prompt string) ([]string, error) {
	tokens := []string{}
	for _, match := range tokenPattern.FindAllStringSubmatch(prompt, -1) {
		token := strings.TrimSpace(match[1])
		parts := strings.Split(token, ".")
		valid := len(parts) == 1 && model.ValidIdentifier(token)
		if len(parts) == 2 {
			valid = model.ValidID(parts[0]) && model.ValidIdentifier(parts[1])
		}
		if !valid {
			return nil, fmt.Errorf("некорректная ссылка %q", match[0])
		}
		tokens = append(tokens, token)
	}

	remainder := tokenPattern.ReplaceAllString(prompt, "")
	if strings.Contains(remainder, "{{") || strings.Contains(remainder, "}}") {
		return nil, errors.New("незакрытая или вложенная ссылка в промпте")
	}

	return tokens, nil
}

// ResolveInputs combines the immutable Git context with successful predecessor results.
func ResolveInputs(p model.Pipeline, n model.Node, gitContext model.GitContext, runs map[string]*model.NodeRun) (map[string]string, error) {
	refs, ambiguous, problems := inputReferences(p, n)
	if len(problems) > 0 {
		return nil, errors.New(strings.Join(problems, "; "))
	}

	contextJSON, err := json.MarshalIndent(gitContext, "", "  ")
	if err != nil {
		return nil, err
	}
	prJSON, err := json.MarshalIndent(gitContext.PR, "", "  ")
	if err != nil {
		return nil, err
	}
	inputs := map[string]string{
		"context": string(contextJSON), "repository": gitContext.Repository.Path,
		"branch": gitContext.Branch, "commit": gitContext.SHA, "pull_request": string(prJSON),
	}
	nodes := map[string]model.Node{}
	for _, node := range p.Nodes {
		nodes[node.ID] = node
	}

	for token, source := range refs {
		if ambiguous[token] || model.IsBuiltinToken(token) {
			continue
		}
		previous := runs[source.NodeID]
		if previous == nil || previous.Status != "success" {
			return nil, fmt.Errorf("блок %q ещё не завершён успешно", source.NodeID)
		}
		output, exists := outputByID(nodes[source.NodeID], source.OutputID)
		if !exists {
			return nil, fmt.Errorf("отсутствует результат %q", source.OutputID)
		}
		value, exists := previous.Outputs[output.Name]
		if !exists {
			return nil, fmt.Errorf("блок %q не вернул результат %q", source.NodeID, output.Name)
		}
		inputs[token] = value
	}

	if n.ExecutionType() == "ai" {
		if _, err := RenderPrompt(n.Prompt, inputs); err != nil {
			return nil, err
		}
	}

	return inputs, nil
}

// RenderPrompt performs a single substitution pass. Result text is never parsed as a template.
func RenderPrompt(prompt string, inputs map[string]string) (string, error) {
	tokens, err := promptTokens(prompt)
	if err != nil {
		return "", err
	}
	for _, token := range tokens {
		if _, exists := inputs[token]; !exists {
			return "", fmt.Errorf("отсутствующий или неоднозначный вход {{%s}}", token)
		}
	}

	return tokenPattern.ReplaceAllStringFunc(prompt, func(raw string) string {
		return inputs[strings.TrimSpace(raw[2:len(raw)-2])]
	}), nil
}

// RenameOutput preserves stable bindings and rewrites only references to this output.
// When the new shorthand would collide, the qualified token preserves its meaning.
func RenameOutput(p *model.Pipeline, nodeID, outputID, newName string) error {
	if p == nil {
		return errors.New("пайплайн отсутствует")
	}
	if !model.ValidIdentifier(newName) || model.IsBuiltinToken(newName) {
		return errors.New("некорректное или зарезервированное имя результата")
	}

	candidate := *p
	candidate.Nodes = append([]model.Node(nil), p.Nodes...)
	var oldName string
	found := false
	for i, n := range candidate.Nodes {
		if n.ID != nodeID {
			continue
		}
		for _, output := range n.Outputs {
			if output.ID != outputID && output.Name == newName {
				return errors.New("такое имя результата уже существует у блока")
			}
		}
		candidate.Nodes[i].Outputs = append([]model.Output(nil), n.Outputs...)
		for j, output := range n.Outputs {
			if output.ID == outputID {
				oldName, found = output.Name, true
				if n.ExecutionType() == "cmd" {
					candidate.Nodes[i].Outputs[j].Source = output.StreamSource()
				}
				candidate.Nodes[i].Outputs[j].Name = newName
			}
		}
	}
	if !found {
		return errors.New("блок или результат не найден")
	}
	if oldName == newName {
		return nil
	}

	target := model.Source{NodeID: nodeID, OutputID: outputID}
	for i, n := range p.Nodes {
		before, ambiguousBefore, _ := inputReferences(*p, n)
		after, ambiguousAfter, _ := inputReferences(candidate, candidate.Nodes[i])
		var renameErr error
		rewrite := func(raw string) string {
			token := strings.TrimSpace(raw[2 : len(raw)-2])
			if ambiguousBefore[token] && token == oldName {
				renameErr = fmt.Errorf("блок %q: сначала разрешите неоднозначную ссылку {{%s}}", n.ID, token)
				return raw
			}
			source, exists := before[token]
			if !exists || ambiguousBefore[token] || model.IsBuiltinToken(token) {
				return raw
			}
			if _, alias := n.Inputs[token]; alias {
				return raw
			}

			replacement := token
			if source == target {
				replacement = newName
				if strings.Contains(token, ".") || ambiguousAfter[newName] || after[newName] != target {
					replacement = nodeID + "." + newName
				}
			} else if !strings.Contains(token, ".") && (ambiguousAfter[token] || after[token] != source) {

				replacement = source.NodeID + "." + token
			}

			return strings.Replace(raw, token, replacement, 1)
		}
		rewriteText := func(value string) (string, error) {
			updated := tokenPattern.ReplaceAllStringFunc(value, rewrite)

			return updated, renameErr
		}
		if n.ExecutionType() == "ai" {
			candidate.Nodes[i].Prompt, _ = rewriteText(n.Prompt)
		}
		if n.InputJSON != nil {
			changed := false
			data, err := transformInputJSON(n.InputJSON, func(value string) (string, error) {
				updated, err := rewriteText(value)
				changed = changed || updated != value

				return updated, err
			})
			if err != nil {
				return fmt.Errorf("блок %q: %w", n.ID, err)
			}
			if changed {
				encoded, err := json.MarshalIndent(data, "", "  ")
				if err != nil {
					return err
				}
				text := string(encoded)
				candidate.Nodes[i].InputJSON = &text
			}
		}
		if renameErr != nil {
			return renameErr
		}
	}
	*p = candidate

	return nil
}
