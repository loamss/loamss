/**
 * Unit tests for the RSS parser. Network-driven sync is covered
 * via the runtime's integration suite; here we just pin parsing
 * behavior against captured fixtures.
 */

import { test, expect } from "bun:test";
import { parseRSS } from "./index.js";

test("parseRSS: standard RSS 2.0 with guids", () => {
	const xml = `<?xml version="1.0"?>
<rss version="2.0">
  <channel>
    <title>Example</title>
    <item>
      <title>First post</title>
      <link>https://example.com/first</link>
      <guid>https://example.com/first</guid>
      <pubDate>Mon, 13 May 2024 13:00:00 GMT</pubDate>
      <description>First body text.</description>
    </item>
    <item>
      <title>Second post</title>
      <link>https://example.com/second</link>
      <guid>https://example.com/second</guid>
      <pubDate>Tue, 14 May 2024 13:00:00 GMT</pubDate>
    </item>
  </channel>
</rss>`;
	const items = parseRSS(xml);
	expect(items).toHaveLength(2);
	expect(items[0]!.title).toBe("First post");
	expect(items[0]!.guid).toBe("https://example.com/first");
	expect(items[0]!.link).toBe("https://example.com/first");
	expect(items[0]!.description).toBe("First body text.");
	expect(items[0]!.pubDate).toMatch(/^2024-05-13T/);
	expect(items[1]!.guid).toBe("https://example.com/second");
});

test("parseRSS: Atom feed with id + updated", () => {
	const xml = `<?xml version="1.0"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>Example Atom</title>
  <entry>
    <id>tag:example.com,2024:1</id>
    <title>Atom one</title>
    <link href="https://example.com/atom1"/>
    <updated>2024-06-01T10:00:00Z</updated>
    <summary>Atom summary.</summary>
  </entry>
</feed>`;
	const items = parseRSS(xml);
	expect(items).toHaveLength(1);
	expect(items[0]!.guid).toBe("tag:example.com,2024:1");
	expect(items[0]!.link).toBe("https://example.com/atom1");
	expect(items[0]!.description).toBe("Atom summary.");
	expect(items[0]!.pubDate).toBe("2024-06-01T10:00:00.000Z");
});

test("parseRSS: strips CDATA from title + description", () => {
	const xml = `<rss><channel><item>
    <title><![CDATA[Tricky <Title> & Co.]]></title>
    <description><![CDATA[Body with <em>markup</em>.]]></description>
    <guid>g1</guid>
  </item></channel></rss>`;
	const items = parseRSS(xml);
	expect(items[0]!.title).toBe("Tricky <Title> & Co.");
	expect(items[0]!.description).toBe("Body with <em>markup</em>.");
});

test("parseRSS: empty feed returns empty array", () => {
	const xml = `<rss><channel><title>Nothing</title></channel></rss>`;
	expect(parseRSS(xml)).toEqual([]);
});

test("parseRSS: non-ISO pubDate stays as the original string", () => {
	const xml = `<rss><channel><item>
    <title>Weird date</title>
    <guid>g</guid>
    <pubDate>some-random-string</pubDate>
  </item></channel></rss>`;
	const items = parseRSS(xml);
	expect(items[0]!.pubDate).toBe("some-random-string");
});
