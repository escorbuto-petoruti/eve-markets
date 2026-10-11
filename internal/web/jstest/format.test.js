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
