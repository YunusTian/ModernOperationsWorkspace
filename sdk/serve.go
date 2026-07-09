package sdk

import "errors"

// -----------------------------------------------------------------------------
// Validate：静态校验（Serve 与外部单测均可复用）
// -----------------------------------------------------------------------------

// Validate 对插件的静态定义做完整性校验。
// 建议在单元测试中主动调用，尽早暴露错误。
func Validate(p Plugin) error {
	if p == nil {
		return errors.New("sdk: plugin is nil")
	}

	m := p.Metadata()
	if m.ID == "" {
		return errors.New("sdk: metadata.ID is required")
	}
	if m.Name == "" {
		return errors.New("sdk: metadata.Name is required")
	}
	if m.Version == "" {
		return errors.New("sdk: metadata.Version is required")
	}

	seen := map[string]struct{}{}
	for _, h := range p.Commands() {
		if h == nil {
			return errors.New("sdk: nil CommandHandler")
		}
		spec := h.Spec()
		if spec.ID == "" {
			return errors.New("sdk: command.ID is required")
		}
		if _, dup := seen[spec.ID]; dup {
			return errors.New("sdk: duplicate command id: " + spec.ID)
		}
		seen[spec.ID] = struct{}{}

		if spec.Permission == PermUnspecified {
			return errors.New("sdk: command must declare permission: " + spec.ID)
		}
	}

	return nil
}
