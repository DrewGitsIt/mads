package hermescli

import "github.com/drewkidwell/mads/providers/provider"

// hermesToolKinds is hermes's TOOL_KIND_MAP (acp_adapter/tools.py,
// get_tool_kind): the ACP kind its tool_call carries for each tool, and so
// the kind a stored call is shown with. Anything unlisted is "other".
var hermesToolKinds = func() map[string]provider.ToolKind {
	m := map[string]provider.ToolKind{}
	for kind, names := range map[provider.ToolKind][]string{
		provider.ToolKindRead: {"read_file", "skill_view", "skills_list", "browser_snapshot", "browser_vision",
			"browser_get_images", "vision_analyze"},
		provider.ToolKindEdit:   {"write_file", "patch", "skill_manage"},
		provider.ToolKindSearch: {"search_files"},
		provider.ToolKindExecute: {"terminal", "process", "execute_code", "browser_click", "browser_type", "browser_scroll",
			"browser_press", "browser_back", "delegate_task", "image_generate", "text_to_speech"},
		provider.ToolKindFetch: {"web_search", "web_extract", "browser_navigate"},
		provider.ToolKindOther: {"todo"},
		provider.ToolKindThink: {"_thinking"},
	} {
		for _, n := range names {
			m[n] = kind
		}
	}
	return m
}()

func hermesToolKind(name string) provider.ToolKind {
	if k, ok := hermesToolKinds[name]; ok {
		return k
	}
	return provider.ToolKindOther
}
