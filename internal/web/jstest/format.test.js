"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");

const f = require("../static/format.js");

test("fmtInt groups thousands and drops decimals (en-US)", () => {
  assert.equal(f.fmtInt(0), "0");
  assert.equal(f.fmtInt(1234567), "1,234,567");
  assert.equal(f.fmtInt(1234.6), "1,235");
  assert.equal(f.fmtInt(-1500), "-1,500");
});

test("fmtIsk always shows two decimals (en-US)", () => {
  assert.equal(f.fmtIsk(0), "0.00");
  assert.equal(f.fmtIsk(5), "5.00");
  assert.equal(f.fmtIsk(1234567.891), "1,234,567.89");
});

test("fmtPrice hides non-positive prices", () => {
  assert.equal(f.fmtPrice(0), "-");
  assert.equal(f.fmtPrice(-3), "-");
  assert.equal(f.fmtPrice(undefined), "-");
  assert.equal(f.fmtPrice(1500.5), "1,500.50");
});

test("fmtPct handles missing values", () => {
  assert.equal(f.fmtPct(null), "-");
  assert.equal(f.fmtPct(undefined), "-");
});

test("fmtPct signs and rounds to one decimal", () => {
  assert.equal(f.fmtPct(12.345), "+12.3%");
  assert.equal(f.fmtPct(0), "0.0%");
  assert.equal(f.fmtPct(-4.56), "-4.6%");
});

test("fmtShare renders a percentage of the total with one decimal", () => {
  assert.equal(f.fmtShare(96, 1000), "9.6%");
  assert.equal(f.fmtShare(0, 1000), "0.0%");
  assert.equal(f.fmtShare(1000, 1000), "100.0%");
  assert.equal(f.fmtShare(1, 3), "33.3%");
});

test("fmtShare shows '-' without a positive total", () => {
  assert.equal(f.fmtShare(5, 0), "-");
  assert.equal(f.fmtShare(5, undefined), "-");
  assert.equal(f.fmtShare(5, null), "-");
  assert.equal(f.fmtShare(5, -1), "-");
});

test("fmtJita returns '-' without a Jita comparison", () => {
  assert.equal(f.fmtJita({}, "best_sell", false), "-");
  assert.equal(f.fmtJita({ jita: null }, "sell_pct", true), "-");
});

test("fmtJita formats prices and percentages", () => {
  const r = { jita: { best_sell: 1000.5, best_buy: 0, sell_pct: 5.56, buy_pct: -2 } };
  assert.equal(f.fmtJita(r, "best_sell", false), "1,000.50");
  assert.equal(f.fmtJita(r, "best_buy", false), "-");
  assert.equal(f.fmtJita(r, "sell_pct", true), "+5.6%");
  assert.equal(f.fmtJita(r, "buy_pct", true), "-2.0%");
  assert.equal(f.fmtJita(r, "missing", true), "-");
});

test("fmtTime renders epoch seconds with the runtime locale", () => {
  assert.equal(f.fmtTime(0), "-");
  assert.equal(f.fmtTime(undefined), "-");
  // toLocaleString depends on the machine locale and time zone, so compare
  // against the same call instead of a fixed string.
  assert.equal(f.fmtTime(1700000000), new Date(1700000000 * 1000).toLocaleString());
});

test("fmtVolume shows m3 with up to two decimals and hides non-positive volumes", () => {
  assert.equal(f.fmtVolume(0), "-");
  assert.equal(f.fmtVolume(undefined), "-");
  assert.equal(f.fmtVolume(0.005), "0.01");
  assert.equal(f.fmtVolume(1234567.891), "1,234,567.89");
});

test("fmtIskPerM3 divides value by volume and hides a missing volume", () => {
  assert.equal(f.fmtIskPerM3(1000, 4), "250.00");
  assert.equal(f.fmtIskPerM3(1000, 0), "-");
  assert.equal(f.fmtIskPerM3(1000, undefined), "-");
});

test("groupByCategory totals each category, biggest sold value first, unknown last", () => {
  const rows = [
    { type_id: 1, category: "Charge", sold_units: 10, sold_value: 100, sold_volume: 1, open_buy_value: 5 },
    { type_id: 2, category: "", sold_units: 1, sold_value: 900, sold_volume: 0, open_buy_value: 0 },
    { type_id: 3, category: "Material", sold_units: 5, sold_value: 300, sold_volume: 2, open_buy_value: 0 },
    { type_id: 4, category: "Charge", sold_units: 20, sold_value: 250, sold_volume: 3, open_buy_value: 7 }
  ];
  const groups = f.groupByCategory(rows);
  assert.deepEqual(groups.map((g) => g.name), ["Charge", "Material", "Unknown"]);
  const charge = groups[0];
  assert.equal(charge.sold_units, 30);
  assert.equal(charge.sold_value, 350);
  assert.equal(charge.sold_volume, 4);
  assert.equal(charge.open_buy_value, 12);
  assert.deepEqual(charge.rows.map((r) => r.type_id), [1, 4]);
  assert.deepEqual(f.groupByCategory([]), []);
});

test("filterRows keeps rows whose name, category or group contains the query", () => {
  const rows = [
    { type_id: 1, name: "Tritanium", category: "Material", group: "Mineral" },
    { type_id: 2, name: "Large Skill Injector", category: "Skill", group: "Booster" },
    { type_id: 3, name: "", category: "Charge", group: "Missile" }
  ];
  const ids = (q) => f.filterRows(rows, q).map((r) => r.type_id);
  assert.deepEqual(ids("  TRIT "), [1]);
  assert.deepEqual(ids("skill"), [2]);
  assert.deepEqual(ids("missile"), [3]);
  assert.deepEqual(ids(""), [1, 2, 3]);
  assert.deepEqual(ids("zzz"), []);
});

test("sortRows orders by a value function, keeps ties stable and puts missing values last", () => {
  const rows = [
    { id: "a", v: 2 }, { id: "b", v: null }, { id: "c", v: 5 }, { id: "d", v: 2 }
  ];
  const val = (r) => r.v;
  assert.deepEqual(f.sortRows(rows, val, "desc").map((r) => r.id), ["c", "a", "d", "b"]);
  assert.deepEqual(f.sortRows(rows, val, "asc").map((r) => r.id), ["a", "d", "c", "b"]);
  assert.deepEqual(rows.map((r) => r.id), ["a", "b", "c", "d"], "input is not mutated");
  const named = [{ n: "pear" }, { n: "Apple" }, { n: "fig" }];
  assert.deepEqual(f.sortRows(named, (r) => r.n, "asc").map((r) => r.n), ["Apple", "fig", "pear"]);
});
