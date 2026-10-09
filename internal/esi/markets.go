package esi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// maxMarketPages bounds how many pages one RegionOrders call follows.
const maxMarketPages = 100

// ErrInvalidInput is returned, before any request, for an id or order type
// that ESI would reject.
var ErrInvalidInput = errors.New("esi: invalid input")

// Order is one market order as ESI reports it.
type Order struct {
	OrderID      int64   `json:"order_id"`
	TypeID       int     `json:"type_id"`
	LocationID   int64   `json:"location_id"`
	IsBuyOrder   bool    `json:"is_buy_order"`
	Price        float64 `json:"price"`
	VolumeRemain int64   `json:"volume_remain"`
	VolumeTotal  int64   `json:"volume_total"`
	MinVolume    int64   `json:"min_volume"`
	Range        string  `json:"range"`
	Duration     int     `json:"duration"`
	Issued       string  `json:"issued"`
}

// HistoryDay is the daily market statistics of one type in a region.
type HistoryDay struct {
	Date       string  `json:"date"`
	Average    float64 `json:"average"`
	Highest    float64 `json:"highest"`
	Lowest     float64 `json:"lowest"`
	OrderCount int64   `json:"order_count"`
	Volume     int64   `json:"volume"`
}

// RegionOrders returns the public orders of a region, following X-Pages up to
// maxMarketPages. orderType is buy, sell or all; typeID 0 means every type. It
// sends no token.
func (c *Client) RegionOrders(ctx context.Context, regionID int, orderType string, typeID int) ([]Order, error) {
	switch {
	case regionID <= 0:
		return nil, fmt.Errorf("%w: region id must be positive", ErrInvalidInput)
	case orderType != "buy" && orderType != "sell" && orderType != "all":
		return nil, fmt.Errorf("%w: order type must be buy, sell or all", ErrInvalidInput)
	case typeID < 0:
		return nil, fmt.Errorf("%w: type id must not be negative", ErrInvalidInput)
	}
	path := "/markets/" + strconv.Itoa(regionID) + "/orders/"
	var all []Order
	pages := 1
	for page := 1; page <= pages && page <= maxMarketPages; page++ {
		q := url.Values{"order_type": {orderType}, "page": {strconv.Itoa(page)}}
		if typeID > 0 {
			q.Set("type_id", strconv.Itoa(typeID))
		}
		body, hdr, err := c.get(ctx, "", path, q)
		if err != nil {
			return nil, err
		}
		var rows []Order
		if err := json.Unmarshal(body, &rows); err != nil {
			return nil, fmt.Errorf("esi: decode region orders: %w", err)
		}
		all = append(all, rows...)
		if n, err := strconv.Atoi(strings.TrimSpace(hdr.Get("X-Pages"))); err == nil && n > pages {
			pages = n
		}
	}
	return all, nil
}

// RegionHistory returns the daily history of a type in a region. It sends no
// token.
func (c *Client) RegionHistory(ctx context.Context, regionID, typeID int) ([]HistoryDay, error) {
	if regionID <= 0 || typeID <= 0 {
		return nil, fmt.Errorf("%w: region id and type id must be positive", ErrInvalidInput)
	}
	body, _, err := c.get(ctx, "", "/markets/"+strconv.Itoa(regionID)+"/history/", url.Values{"type_id": {strconv.Itoa(typeID)}})
	if err != nil {
		return nil, err
	}
	var rows []HistoryDay
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("esi: decode region history: %w", err)
	}
	return rows, nil
}
