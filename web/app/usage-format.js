// Shared usage formatting. Credits are a subscription meter (Kiro CLI):
// always two decimals, never priced and never part of cost. Token-only usage
// keeps every existing token rendering path unchanged; these helpers only add
// credits when a usage actually carries them.
function formatCredits(n){n=Number(n);return (Number.isFinite(n)&&n>0?n:0).toFixed(2)}
function usageCredits(u){const n=Number(u?.credits);return Number.isFinite(n)&&n>0?n:0}
function usageHasTokens(u){return ['totalTokens','inputTokens','outputTokens'].some(k=>Number(u?.[k])>0)}
// Headline amount: tokens unless the usage is credits-only, plus credits when present.
// With no credits this is exactly `${formatTokens(tokens)} tokens`.
function usageAmountText(tokens,credits,hasTokens){
  const c=Number(credits)>0,t=hasTokens??Number(tokens)>0;
  return [...(t||!c?[`${formatTokens(tokens)} tokens`]:[]),...(c?[`${formatCredits(credits)} credits`]:[])].join(' · ');
}
