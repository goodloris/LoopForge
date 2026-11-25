// Credits are a subscription meter (Kiro CLI): two decimals, never priced.
// Same rule and test vectors as web/app/usage-format.js
// (tests/fixtures/credits-format-vectors.json).
export function formatCredits(n) {
  n = Number(n);
  return (Number.isFinite(n) && n > 0 ? n : 0).toFixed(2);
}
