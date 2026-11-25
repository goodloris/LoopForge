import React from "react";
import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, test } from "vitest";
import MessageInfo from "../src/MessageInfo.jsx";
import { formatCredits } from "../src/usage-format.js";
import vectors from "./fixtures/credits-format-vectors.json";


function openCard(message) {
  render(<MessageInfo message={message} sessionExecutor="kiro" />);
  fireEvent.click(screen.getByRole("button", { name: "统计信息" }));
  return document.querySelector(".message-info-card");
}

describe("credits in message details", () => {
  test("formatCredits matches the shared vectors", () => {
    for (const { input, text } of vectors) expect(formatCredits(input)).toBe(text);
  });

  test("token-only replies render no credits row", () => {
    const card = openCard({ usage: { inputTokens: 4, outputTokens: 6, totalTokens: 10 } });
    expect(card.querySelector('[data-key="credits"]')).toBeNull();
    expect(card.querySelector(".message-info-total strong").textContent).toBe("Tokens");
    expect(card.querySelectorAll(".message-info-parts > div")).toHaveLength(4);
  });

  test("Kiro replies render credits and hide the zero token block", () => {
    const card = openCard({ usage: { credits: 12.34 } });
    const row = card.querySelector('[data-key="credits"]');
    expect(row.textContent).toBe("Credits12.34");
    expect(card.textContent).not.toContain("Tokens");
  });

  test("mixed replies render both tokens and credits", () => {
    const card = openCard({ usage: { inputTokens: 100, outputTokens: 20, totalTokens: 120, credits: 0.5 } });
    expect(card.textContent).toContain("Tokens");
    expect(card.querySelector('[data-key="credits"] span').textContent).toBe("0.50");
  });
});
