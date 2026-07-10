// loader.go 负责将 YAML DSL 反序列化为 Workflow。
//
// v0.2 PR2：仅提供严格模式解析（未知字段报错），不涉及变量求值。
// 顶层结构参考 docs/workflow.md：
//
//	workflow:
//	  id: deploy.dotnet
//	  inputs: [...]
//	  steps:  [...]
//
// 解析后会调用 Workflow.Validate 做一次静态校验，避免调用方拿到半成品。

package workflow

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// yamlDoc 是顶层文档结构：workflow: ...
type yamlDoc struct {
	Workflow *yamlWorkflow `yaml:"workflow"`
}

type yamlWorkflow struct {
	ID          string       `yaml:"id"`
	Name        string       `yaml:"name"`
	Description string       `yaml:"description"`
	Inputs      []yamlInput  `yaml:"inputs"`
	Steps       []yamlStep   `yaml:"steps"`
}

type yamlInput struct {
	Name        string `yaml:"name"`
	Type        string `yaml:"type"`
	Required    bool   `yaml:"required"`
	Default     any    `yaml:"default"`
	Description string `yaml:"description"`
}

type yamlStep struct {
	ID      string         `yaml:"id"`
	Command string         `yaml:"command"`
	Recipe  string         `yaml:"recipe"`
	Params  map[string]any `yaml:"params"`
	Timeout string         `yaml:"timeout"`
	When    string         `yaml:"when"`
	Retry   *yamlRetry     `yaml:"retry"`
}

// yamlRetry 是 retry: { ... } 的原始形态。所有字段均可选。
//
// 用字符串接 duration 是为了让 YAML 里写 "500ms" / "2s" 直观；数字则按 Max 处理。
type yamlRetry struct {
	Max         int    `yaml:"max"`
	Backoff     string `yaml:"backoff"`
	MaxBackoff  string `yaml:"max_backoff"`
	Exponential bool   `yaml:"exponential"`
}

// LoadFile 从文件路径加载并解析 Workflow。
func LoadFile(path string) (*Workflow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("workflow: open %s: %w", path, err)
	}
	defer f.Close()
	return LoadReader(f)
}

// LoadBytes 从字节切片解析 Workflow。
func LoadBytes(data []byte) (*Workflow, error) {
	return LoadReader(bytes.NewReader(data))
}

// LoadReader 从 io.Reader 解析 Workflow。
//
// 严格模式：任何未声明字段（拼写错误、遗留字段等）都会返回错误。
func LoadReader(r io.Reader) (*Workflow, error) {
	if r == nil {
		return nil, errors.New("workflow: reader is nil")
	}
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)

	var doc yamlDoc
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("workflow: empty document")
		}
		return nil, fmt.Errorf("workflow: parse yaml: %w", err)
	}
	// 不允许多文档流：进一步读取应返回 EOF。
	var extra yamlDoc
	if err := dec.Decode(&extra); err == nil {
		return nil, errors.New("workflow: multi-document yaml is not supported")
	} else if !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("workflow: parse yaml: %w", err)
	}

	if doc.Workflow == nil {
		return nil, errors.New("workflow: missing top-level 'workflow' key")
	}

	w, err := doc.Workflow.toWorkflow()
	if err != nil {
		return nil, err
	}
	if err := w.Validate(); err != nil {
		return nil, fmt.Errorf("workflow: validate: %w", err)
	}
	return w, nil
}

func (y *yamlWorkflow) toWorkflow() (*Workflow, error) {
	w := &Workflow{
		ID:          y.ID,
		Name:        y.Name,
		Description: y.Description,
	}
	if len(y.Inputs) > 0 {
		w.Inputs = make([]Input, 0, len(y.Inputs))
		for _, yi := range y.Inputs {
			w.Inputs = append(w.Inputs, Input{
				Name:        yi.Name,
				Type:        InputType(yi.Type),
				Required:    yi.Required,
				Default:     yi.Default,
				Description: yi.Description,
			})
		}
	}
	if len(y.Steps) > 0 {
		w.Steps = make([]Step, 0, len(y.Steps))
		for i, ys := range y.Steps {
			step := Step{
				ID:      ys.ID,
				Command: ys.Command,
				Recipe:  ys.Recipe,
				Params:  ys.Params,
				When:    ys.When,
			}
			if ys.Timeout != "" {
				d, err := time.ParseDuration(ys.Timeout)
				if err != nil {
					return nil, fmt.Errorf("workflow: step[%d] timeout: %w", i, err)
				}
				step.Timeout = d
			}
			if ys.Retry != nil {
				rp := &RetryPolicy{Max: ys.Retry.Max, Exponential: ys.Retry.Exponential}
				if ys.Retry.Backoff != "" {
					d, err := time.ParseDuration(ys.Retry.Backoff)
					if err != nil {
						return nil, fmt.Errorf("workflow: step[%d] retry.backoff: %w", i, err)
					}
					rp.Backoff = d
				}
				if ys.Retry.MaxBackoff != "" {
					d, err := time.ParseDuration(ys.Retry.MaxBackoff)
					if err != nil {
						return nil, fmt.Errorf("workflow: step[%d] retry.max_backoff: %w", i, err)
					}
					rp.MaxBackoff = d
				}
				step.Retry = rp
			}
			w.Steps = append(w.Steps, step)
		}
	}
	return w, nil
}
