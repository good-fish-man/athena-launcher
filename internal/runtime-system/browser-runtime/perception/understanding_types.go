package perception

const (
	uiTreeSchema      = "athena.browser.ui-tree.v1"
	pageModelSchema   = "athena.browser.page-model.v1"
	patternSchema     = "athena.browser.pattern.v1"
	interactionSchema = "athena.browser.interaction.v1"
)

// UINode is a site-independent representation of one observable page element.
// It deliberately carries semantic state rather than raw DOM or CSS selectors.
type UINode struct {
	ID       string         `json:"id"`
	Ref      string         `json:"ref,omitempty"`
	ParentID string         `json:"parent_id,omitempty"`
	Role     string         `json:"role"`
	Kind     string         `json:"kind"`
	Name     string         `json:"name,omitempty"`
	URL      string         `json:"url,omitempty"`
	Region   string         `json:"region,omitempty"`
	Order    int            `json:"order"`
	Box      *BoundingBox   `json:"box,omitempty"`
	State    map[string]any `json:"state,omitempty"`
	Signals  []string       `json:"signals,omitempty"`
}

// UITree fuses Accessibility, ARIA, focused DOM, and observed geometry into a
// bounded tree that remains stable across websites.
type UITree struct {
	Schema           string   `json:"schema"`
	RootID           string   `json:"root_id"`
	Nodes            []UINode `json:"nodes"`
	InteractiveCount int      `json:"interactive_count"`
	LocatedCount     int      `json:"located_count"`
}

type Pattern struct {
	Schema     string         `json:"schema"`
	ID         string         `json:"id"`
	Kind       string         `json:"kind"`
	Label      string         `json:"label,omitempty"`
	Confidence float64        `json:"confidence"`
	NodeRefs   []string       `json:"node_refs,omitempty"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

type SemanticEntity struct {
	ID         string         `json:"id"`
	Kind       string         `json:"kind"`
	Label      string         `json:"label"`
	Ref        string         `json:"ref,omitempty"`
	URL        string         `json:"url,omitempty"`
	Position   int            `json:"position"`
	SectionID  string         `json:"section_id,omitempty"`
	Playable   bool           `json:"playable,omitempty"`
	Confidence float64        `json:"confidence"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

type SemanticSection struct {
	ID        string   `json:"id"`
	Kind      string   `json:"kind"`
	Label     string   `json:"label,omitempty"`
	EntityIDs []string `json:"entity_ids,omitempty"`
}

// Interaction is an abstract, low-level-safe action candidate. The Browser
// Runtime maps it to browser.click/type/play; the model never needs selectors.
type Interaction struct {
	Schema        string         `json:"schema"`
	ID            string         `json:"id"`
	Kind          string         `json:"kind"`
	Label         string         `json:"label"`
	Description   string         `json:"description,omitempty"`
	Ref           string         `json:"ref,omitempty"`
	TargetURL     string         `json:"target_url,omitempty"`
	EntityID      string         `json:"entity_id,omitempty"`
	InputRef      string         `json:"input_ref,omitempty"`
	Risk          string         `json:"risk"`
	Confidence    float64        `json:"confidence"`
	Precondition  map[string]any `json:"precondition,omitempty"`
	Postcondition map[string]any `json:"postcondition,omitempty"`
}

type SemanticPageModel struct {
	Schema         string            `json:"schema"`
	Type           string            `json:"type"`
	Confidence     float64           `json:"confidence"`
	Title          string            `json:"title,omitempty"`
	URL            string            `json:"url,omitempty"`
	Sections       []SemanticSection `json:"sections,omitempty"`
	Entities       []SemanticEntity  `json:"entities,omitempty"`
	Interactions   []Interaction     `json:"interactions,omitempty"`
	PatternIDs     []string          `json:"pattern_ids,omitempty"`
	Signals        []string          `json:"signals,omitempty"`
	RequiresVisual bool              `json:"requires_visual,omitempty"`
}
