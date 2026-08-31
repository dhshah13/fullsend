import { describe, expect, it } from "vitest";
import { parseSearchQuery, textContainsPhrases } from "./searchQuery";

describe("parseSearchQuery", () => {
  it("returns the raw query and no phrases when there are no quotes", () => {
    expect(parseSearchQuery("eval scenario")).toEqual({
      query: "eval scenario",
      phrases: [],
    });
  });

  it("extracts a single quoted phrase", () => {
    expect(parseSearchQuery('"eval scenario"')).toEqual({
      query: "eval scenario",
      phrases: ["eval scenario"],
    });
  });

  it("extracts a phrase surrounded by unquoted terms", () => {
    expect(parseSearchQuery('harness "eval scenario" config')).toEqual({
      query: "harness eval scenario config",
      phrases: ["eval scenario"],
    });
  });

  it("extracts multiple quoted phrases", () => {
    expect(parseSearchQuery('"eval scenario" "harness config"')).toEqual({
      query: "eval scenario harness config",
      phrases: ["eval scenario", "harness config"],
    });
  });

  it("ignores empty quoted strings", () => {
    expect(parseSearchQuery('foo "" bar')).toEqual({
      query: 'foo "" bar',
      phrases: [],
    });
  });

  it("treats unmatched quotes as literal characters", () => {
    expect(parseSearchQuery('"eval scenario')).toEqual({
      query: '"eval scenario',
      phrases: [],
    });
  });

  it("handles a single unquoted word", () => {
    expect(parseSearchQuery("harness")).toEqual({
      query: "harness",
      phrases: [],
    });
  });

  it("handles an empty string", () => {
    expect(parseSearchQuery("")).toEqual({
      query: "",
      phrases: [],
    });
  });

  it("trims whitespace inside quoted phrases", () => {
    expect(parseSearchQuery('"  eval scenario  "')).toEqual({
      query: "eval scenario",
      phrases: ["eval scenario"],
    });
  });

  it("handles a quoted single word", () => {
    expect(parseSearchQuery('"harness"')).toEqual({
      query: "harness",
      phrases: ["harness"],
    });
  });
});

describe("textContainsPhrases", () => {
  it("returns true when there are no phrases", () => {
    expect(textContainsPhrases("any text", [])).toBe(true);
  });

  it("returns true when the phrase appears in the text", () => {
    expect(
      textContainsPhrases("The eval scenario runner starts here.", ["eval scenario"]),
    ).toBe(true);
  });

  it("returns false when the phrase does not appear adjacent", () => {
    expect(
      textContainsPhrases("The eval of each scenario is different.", ["eval scenario"]),
    ).toBe(false);
  });

  it("matches case-insensitively", () => {
    expect(textContainsPhrases("The Eval Scenario runner.", ["eval scenario"])).toBe(true);
  });

  it("requires all phrases to match", () => {
    expect(
      textContainsPhrases("eval scenario and harness config details", [
        "eval scenario",
        "harness config",
      ]),
    ).toBe(true);

    expect(
      textContainsPhrases("eval scenario but no harness here", [
        "eval scenario",
        "harness config",
      ]),
    ).toBe(false);
  });

  it("returns true for a single-word phrase that appears in text", () => {
    expect(textContainsPhrases("the harness is ready", ["harness"])).toBe(true);
  });

  it("returns false when text is empty and phrases are not", () => {
    expect(textContainsPhrases("", ["eval scenario"])).toBe(false);
  });
});
