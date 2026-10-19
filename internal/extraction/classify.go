package extraction

import (
	"path"
	"strings"
)

// NodeClass is one closure node's export classification.
type NodeClass struct {
	ID string
	// Reason records which rule decided it, so an "other" bucket can be acted
	// on instead of silently growing.
	Reason string
}

// ClassifyNode derives a node's category from declarations, never from a
// hard-coded list: an edge may name its category, otherwise the relation it
// resolves against decides, otherwise the target's own extension. Anything
// unclaimed lands in OtherCategory with the reason recorded.
func (c *Catalog) ClassifyNode(node Node) NodeClass {
	if edge, ok := c.EdgeByID(node.Via.EdgeID); ok && edge != nil {
		if edge.Category != "" {
			return NodeClass{ID: edge.Category, Reason: "边 " + edge.ID + " 声明的类别"}
		}
		if edge.Target.Relation != "" {
			if id, ok := c.categoryForRelation(edge.Target.Relation); ok {
				return NodeClass{ID: id, Reason: "relation " + edge.Target.Relation}
			}
		}
	}
	ext := strings.ToLower(path.Ext(node.Path))
	if id, ok := c.categoryForExtension(ext); ok {
		return NodeClass{ID: id, Reason: "目标扩展名 " + ext}
	}
	if node.Via.EdgeID != "" {
		return NodeClass{ID: OtherCategory, Reason: "边 " + node.Via.EdgeID + " 与扩展名 " + ext + " 都未归类"}
	}
	return NodeClass{ID: OtherCategory, Reason: "种子，扩展名 " + ext + " 未归类"}
}
