package esi

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
)

// Type is the part of a public /universe/types entry the demand view needs.
// PackagedVolume is zero when ESI omits it (items that cannot be repackaged).
type Type struct {
	Name           string  `json:"name"`
	GroupID        int64   `json:"group_id"`
	Volume         float64 `json:"volume"`
	PackagedVolume float64 `json:"packaged_volume"`
}

// Group is a public /universe/groups entry.
type Group struct {
	Name       string `json:"name"`
	CategoryID int64  `json:"category_id"`
}

// Category is a public /universe/categories entry.
type Category struct {
	Name string `json:"name"`
}

// UniverseType reads one item type. It sends no token.
func (c *Client) UniverseType(ctx context.Context, id int64) (Type, error) {
	var out Type
	err := c.universe(ctx, "types", id, &out)
	return out, err
}

// UniverseGroup reads one item group. It sends no token.
func (c *Client) UniverseGroup(ctx context.Context, id int64) (Group, error) {
	var out Group
	err := c.universe(ctx, "groups", id, &out)
	return out, err
}

// UniverseCategory reads one item category. It sends no token.
func (c *Client) UniverseCategory(ctx context.Context, id int64) (Category, error) {
	var out Category
	err := c.universe(ctx, "categories", id, &out)
	return out, err
}

func (c *Client) universe(ctx context.Context, kind string, id int64, dst any) error {
	body, _, err := c.get(ctx, "", "/universe/"+kind+"/"+strconv.FormatInt(id, 10)+"/", nil)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("esi: decode universe %s: %w", kind, err)
	}
	return nil
}
