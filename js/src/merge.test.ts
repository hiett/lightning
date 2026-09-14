import { describe, expect, it } from "vitest";

import { merge, stripKeys, type JsonValue, type MergeValue } from "./merge.js";

/**
 * Every case below was produced by running the real differ -- diff.Diff from
 * diff/diff.go -- over the `previous` and `next` values, so these are the exact
 * bytes a lightning server puts on the wire rather than a reading of the spec.
 *
 * The assertion is stripKeys(merge(previous, diff)) == stripKeys(next): merging
 * has to reproduce the server's new value, up to the __key fields the server
 * removes from anything it sends in full.
 */
const groundTruth: ReadonlyArray<{
  name: string;
  previous: JsonValue;
  diff: JsonValue;
  next: JsonValue;
}> = [
  {
    name: "doc: map example",
    previous: { address: { city: "sf", state: "ca" }, age: 30, name: "bob" },
    // Note `friends`, a field that did not exist before: a new field is a
    // replacement like any other, so the array is wrapped and arrives as
    // [["bob", "charlie"]] rather than bare.
    diff: {
      address: { city: "oakland" },
      age: [],
      friends: [["bob", "charlie"]],
      name: "alice",
    },
    next: {
      address: { city: "oakland", state: "ca" },
      friends: ["bob", "charlie"],
      name: "alice",
    },
  },
  {
    name: "doc: reordering example",
    previous: [0, 1, 2, 3],
    diff: { $: [[1, 3], -1], "3": 4 },
    next: [1, 2, 3, 4],
  },
  {
    name: "doc: __key example",
    previous: [
      { __key: 10, age: 20, name: "bob" },
      { __key: 13, name: "alice" },
    ],
    diff: { $: [1, 0], "1": { age: 23 } },
    next: [
      { __key: 13, name: "alice" },
      { __key: 10, age: 23, name: "bob" },
    ],
  },
  {
    name: "shapes: 0123 -> 3 -1 0 1 4",
    previous: ["0", "1", "2", "3"],
    diff: { $: [3, -1, [0, 2], -1], "1": "-1", "4": "4" },
    next: ["3", "-1", "0", "1", "4"],
  },
  {
    name: "shapes: abcd -> bcde",
    previous: ["a", "b", "c", "d"],
    diff: { $: [[1, 3], -1], "3": "e" },
    next: ["b", "c", "d", "e"],
  },
  {
    name: "shapes: abcd -> zcd",
    previous: ["a", "b", "c", "d"],
    diff: { $: [-1, [2, 2]], "0": "z" },
    next: ["z", "c", "d"],
  },
  {
    name: "shapes: abc -> ca",
    previous: ["a", "b", "c"],
    // Two bare numbers, not a [start, length] run: what makes a run a run is
    // being a nested array, not being a pair.
    diff: { $: [2, 0] },
    next: ["c", "a"],
  },
  {
    name: "shapes: ab -> xy",
    previous: ["a", "b"],
    diff: { $: [-1, -1], "0": "x", "1": "y" },
    next: ["x", "y"],
  },
  {
    name: "shapes: abc -> ab",
    previous: ["a", "b", "c"],
    diff: { $: [[0, 2]] },
    next: ["a", "b"],
  },
  {
    name: "shapes: a -> empty",
    previous: ["a"],
    diff: { $: [] },
    next: [],
  },
  {
    name: "shapes: empty -> a",
    previous: [],
    diff: { $: [-1], "0": "a" },
    next: ["a"],
  },
  {
    name: "initial snapshot (previous nil)",
    previous: null,
    diff: [{ users: [{ age: 20, name: "bob" }, { name: "alice" }] }],
    next: {
      users: [
        { __key: 10, age: 20, name: "bob" },
        { __key: 13, name: "alice" },
      ],
    },
  },
  {
    name: "scalar -> scalar",
    previous: 1,
    diff: 2,
    next: 2,
  },
  {
    name: "value -> null",
    previous: 1,
    // A value becoming null is a replacement, not a deletion.
    diff: [null],
    next: null,
  },
  {
    name: "null -> scalar",
    previous: null,
    diff: "a",
    next: "a",
  },
  {
    name: "map: field removed",
    previous: { a: 1, b: 2 },
    diff: { b: [] },
    next: { a: 1 },
  },
  {
    name: "map: nested recursive",
    previous: { a: { b: 1, c: 2 } },
    diff: { a: { b: 9 } },
    next: { a: { b: 9, c: 2 } },
  },
  {
    name: "map: __key changed in slot",
    previous: { __key: 1, n: "a" },
    diff: [{ n: "b" }],
    next: { __key: 2, n: "b" },
  },
  {
    name: "map: scalar -> object",
    previous: { a: 1 },
    diff: { a: [{ b: 2 }] },
    next: { a: { b: 2 } },
  },
  {
    name: "map: object -> scalar",
    previous: { a: { b: 2 } },
    diff: { a: 1 },
    next: { a: 1 },
  },
  {
    name: "map: array field replaced wholesale",
    previous: { a: [1, 2] },
    diff: { a: "x" },
    next: { a: "x" },
  },
  // A field that did not exist in the previous execution is wrapped by
  // markReplaced like every other replacement. Unwrapped, the four cases below
  // would be read as a deletion, a 1-element replacement, a literal array and a
  // recursive diff respectively -- three of them silently.
  {
    name: "new field: empty array",
    previous: { a: 1 },
    diff: { b: [[]] },
    next: { a: 1, b: [] },
  },
  {
    name: "new field: one-element array",
    previous: { a: 1 },
    diff: { b: [["x"]] },
    next: { a: 1, b: ["x"] },
  },
  {
    name: "new field: longer array",
    previous: { a: 1 },
    diff: { b: [["x", "y"]] },
    next: { a: 1, b: ["x", "y"] },
  },
  {
    name: "new field: object",
    previous: { a: 1 },
    diff: { b: [{ c: 2 }] },
    next: { a: 1, b: { c: 2 } },
  },
  {
    name: "new field: null",
    previous: { a: 1 },
    diff: { b: [null] },
    next: { a: 1, b: null },
  },
  {
    name: "new field: scalar stays bare",
    previous: { a: 1 },
    diff: { b: "x" },
    next: { a: 1, b: "x" },
  },
  {
    name: "new field: keyed object, key stripped by the server",
    previous: { a: 1 },
    diff: { b: [{ c: 2 }] },
    next: { a: 1, b: { __key: 7, c: 2 } },
  },
  {
    name: "new field: nested one execution down",
    previous: { u: { n: "bob" } },
    diff: { u: { tags: [["x"]] } },
    next: { u: { n: "bob", tags: ["x"] } },
  },
  {
    name: "new field: value appearing where there was null",
    previous: { a: null },
    diff: { a: [[1, 2]] },
    next: { a: [1, 2] },
  },
  {
    name: 'object carrying a "$" field',
    previous: { a: { $: 1, b: 2 } },
    // A JSON-valued custom scalar can hold a field named "$", which diffMap
    // diffs like any other. Read as a reordering, this empties the object.
    diff: { a: { $: 2 } },
    next: { a: { $: 2, b: 2 } },
  },
  {
    name: "keyed: append carol",
    previous: [
      { __key: 10, name: "bob" },
      { __key: 13, name: "alice" },
    ],
    // The shape every Relay connection append produces, and the one the Go
    // merge crashes on.
    diff: { $: [[0, 2], -1], "2": [{ name: "carol" }] },
    next: [
      { __key: 10, name: "bob" },
      { __key: 13, name: "alice" },
      { __key: 99, name: "carol" },
    ],
  },
  {
    name: "keyed: prepend + edit",
    previous: [
      { __key: "a", n: 1 },
      { __key: "b", n: 2 },
    ],
    diff: { $: [-1, [0, 2]], "0": [{ n: 3 }], "2": { n: 22 } },
    next: [
      { __key: "c", n: 3 },
      { __key: "a", n: 1 },
      { __key: "b", n: 22 },
    ],
  },
  {
    name: "keyed: remove middle",
    previous: [{ __key: "a" }, { __key: "b" }, { __key: "c" }],
    diff: { $: [0, 2] },
    next: [{ __key: "a" }, { __key: "c" }],
  },
  {
    name: "connection edges",
    previous: {
      viewer: {
        feed: {
          edges: [
            { cursor: "MQ==", node: { __key: "1", id: "1", title: "one" } },
            { cursor: "Mg==", node: { __key: "2", id: "2", title: "two" } },
          ],
          pageInfo: { endCursor: "Mg==", hasNextPage: true },
        },
      },
    },
    // No "$" on `edges`: edge objects carry no __key, so they all hash to the
    // same bucket and line up positionally. The reorder is lost and each slot
    // is repaired by replacing the node instead.
    diff: {
      viewer: {
        feed: {
          edges: {
            "0": { cursor: "Mg==", node: [{ id: "2", title: "TWO" }] },
            "1": { cursor: "Mw==", node: [{ id: "3", title: "three" }] },
          },
          pageInfo: { endCursor: "Mw==", hasNextPage: false },
        },
      },
    },
    next: {
      viewer: {
        feed: {
          edges: [
            { cursor: "Mg==", node: { __key: "2", id: "2", title: "TWO" } },
            { cursor: "Mw==", node: { __key: "3", id: "3", title: "three" } },
          ],
          pageInfo: { endCursor: "Mw==", hasNextPage: false },
        },
      },
    },
  },
  {
    name: "repeated: 1,3 -> 1,1,3",
    previous: [1, 3],
    diff: { $: [0, -1, 1], "1": 1 },
    next: [1, 1, 3],
  },
  {
    name: "repeated: 1,1,3 -> 1,1",
    previous: [1, 1, 3],
    diff: { $: [[0, 2]] },
    next: [1, 1],
  },
  {
    name: "array with nulls and keys",
    previous: [
      { __key: "0" },
      { __key: "1" },
      { __key: "2" },
      { __key: "3" },
      null,
    ],
    diff: { $: [4, 3, -1, [0, 2], -1], "2": [{}], "5": [{}] },
    next: [
      null,
      { __key: "3" },
      { __key: "-1" },
      { __key: "0" },
      { __key: "1" },
      { __key: "4" },
    ],
  },
  {
    name: "array of arrays",
    previous: [["a"], ["b"]],
    diff: { "1": { $: [-1], "0": "c" } },
    next: [["a"], ["c"]],
  },
  {
    name: "deep nested change",
    previous: {
      a: { b: { c: [{ __key: 1, d: "x" }, { __key: 2, d: "y" }] } },
    },
    diff: { a: { b: { c: { "1": { d: "z" } } } } },
    next: {
      a: { b: { c: [{ __key: 1, d: "x" }, { __key: 2, d: "z" }] } },
    },
  },
  {
    name: "bool flip",
    previous: { ok: true },
    diff: { ok: false },
    next: { ok: false },
  },
];

describe("merge against real server diffs", () => {
  for (const testCase of groundTruth) {
    it(testCase.name, () => {
      const merged = merge(testCase.previous, testCase.diff);
      expect(stripKeys(merged)).toEqual(stripKeys(testCase.next));
    });
  }
});

describe("the examples in diff.go's package documentation", () => {
  // old = {"name": "bob", "address": {"state": "ca", "city": "sf"}, "age": 30}
  // new = {"name": "alice", "address": {"state": "ca", "city": "oakland"},
  //        "friends": ["bob", "charlie"]}
  // diff = {"name": "alice", "address": {"city": "oakland"}, "age": [],
  //         "friends": [["bob", "charlie"]]}
  const docMapPrevious = {
    name: "bob",
    address: { state: "ca", city: "sf" },
    age: 30,
  };
  const docMapNext = {
    name: "alice",
    address: { state: "ca", city: "oakland" },
    friends: ["bob", "charlie"],
  };

  it("updates a scalar, recurses into a map, deletes a field and adds a complex one", () => {
    // The diff is the documented one verbatim, which is also what the
    // implementation emits: `friends` is new since the last execution and is
    // wrapped as a replacement, so it does not have to be told apart from a
    // 2-element diff node that cannot exist.
    const merged = merge(docMapPrevious, {
      name: "alice",
      address: { city: "oakland" },
      age: [],
      friends: [["bob", "charlie"]],
    });

    expect(merged).toEqual(docMapNext);
  });

  // old = [0, 1, 2, 3]
  // new = [1, 2, 3, 4]
  // reordering = [1, 2, 3, -1], compressed = [[1, 3], -1]
  it("applies a compressed reordering", () => {
    expect(merge([0, 1, 2, 3], { $: [[1, 3], -1], "3": 4 })).toEqual([
      1, 2, 3, 4,
    ]);
  });

  it("applies the same reordering uncompressed", () => {
    // Compression is optional per entry: a run of one is emitted as a bare
    // number, so both spellings have to mean the same thing.
    expect(merge([0, 1, 2, 3], { $: [1, 2, 3, -1], "3": 4 })).toEqual([
      1, 2, 3, 4,
    ]);
  });

  // old = [{"__key": 10, "name": "bob", "age": 20},
  //        {"__key": 13, "name": "alice"}]
  // new = [{"__key": 13, "name": "alice"},
  //        {"__key": 10, "name": "bob", "age": 23}]
  // diff = {"$": [1, 0], "1": {"age": 23}}
  it("reorders keyed objects and then updates one of them", () => {
    const merged = merge(
      [
        { __key: 10, name: "bob", age: 20 },
        { __key: 13, name: "alice" },
      ],
      { $: [1, 0], "1": { age: 23 } },
    );

    // __key survives in the accumulated value: it is what the next diff's
    // reordering was computed against.
    expect(merged).toEqual([
      { __key: 13, name: "alice" },
      { __key: 10, name: "bob", age: 23 },
    ]);
  });
});

describe("the reorder field", () => {
  it("treats an absent $ as unchanged order, not as an empty array", () => {
    expect(merge(["a", "b", "c"], { "1": "B" })).toEqual(["a", "B", "c"]);
  });

  it('treats "$": [] as an empty array, not as an absent reordering', () => {
    // The trap: [] is truthy in JavaScript, so `update.$ || fallback` happens
    // to work here, and every port that tests emptiness instead gets it wrong.
    expect(merge(["a", "b", "c"], { $: [] })).toEqual([]);
  });

  it("reads [start, length] as a count, not as an end index", () => {
    // [[0, 3], -1] over five elements means indices 0,1,2 -- not 0..3. Reading
    // it as an inclusive end appends a stale trailing element.
    expect(
      merge(["a", "b", "c", "d", "e"], { $: [[0, 3], -1], "3": "X" }),
    ).toEqual(["a", "b", "c", "X"]);
  });

  it("does not lose the tail of a run that does not start at 1", () => {
    expect(merge(["a", "b", "c", "d"], { $: [-1, [2, 2]], "0": "z" })).toEqual([
      "z",
      "c",
      "d",
    ]);
  });

  it("indexes the array after reordering, not before", () => {
    expect(merge(["a", "b"], { $: [1, 0], "0": "B" })).toEqual(["B", "a"]);
  });

  it("leaves a slot empty when a -1 is not filled by the diff", () => {
    // A correct server always fills it. If one does not, the hole becomes null
    // rather than a crash or a silently shortened array.
    expect(stripKeys(merge(["a"], { $: [-1, 0] }))).toEqual([null, "a"]);
  });

  it("survives a source index past the end of the previous array", () => {
    expect(stripKeys(merge(["a"], { $: [5] }))).toEqual([null]);
  });

  it("keeps later slots aligned when a run is malformed", () => {
    // ["x", 2] is not a run anything can be read out of, but it still stands
    // for two slots, and the index keys below it address slots. Dropping it
    // would slide "c" down to index 0 and put the repairs in the wrong places.
    expect(
      stripKeys(
        merge(["a", "b", "c"], { $: [["x", 2], 2], "0": "A", "1": "B" }),
      ),
    ).toEqual(["A", "B", "c"]);
  });

  it("ignores an index key that is not a plain decimal index", () => {
    // Number("") is 0 and Number("01") is 1, so either would otherwise write
    // over a slot nobody addressed.
    expect(stripKeys(merge(["a", "b"], { "": "X" }))).toEqual(["a", "b"]);
    expect(stripKeys(merge(["a", "b"], { "01": "X" }))).toEqual(["a", "b"]);
    expect(stripKeys(merge(["a", "b"], { " 1": "X" }))).toEqual(["a", "b"]);
  });
});

describe("the four diff encodings", () => {
  it("replaces a complex value without merging into it", () => {
    // The replacement is final: `keep` is gone, not preserved.
    expect(merge({ keep: 1, sub: { keep: 2, a: 3 } }, { sub: [{ a: 4 }] })).toEqual(
      { keep: 1, sub: { a: 4 } },
    );
  });

  it("deletes a field given a 0-element array", () => {
    expect(merge({ a: 1, b: { c: 2 } }, { b: [] })).toEqual({ a: 1 });
  });

  it("checks for deletion before recursing", () => {
    // [] is both "no reordering entries" and "delete this field"; which one it
    // is depends on where it appears, and the field position wins.
    expect(merge({ list: ["x"] }, { list: [] })).toEqual({});
  });

  it("replaces a scalar with a bare value", () => {
    expect(merge({ n: 1 }, { n: 2 })).toEqual({ n: 2 });
    expect(merge({ n: "a" }, { n: false })).toEqual({ n: false });
  });

  it("encodes a value becoming null as a replacement, not a deletion", () => {
    expect(merge({ n: 1 }, { n: [null] })).toEqual({ n: null });
  });

  it("merges an empty diff to an empty object", () => {
    // What the server sends on a first subscribe whose result is empty: {} and
    // not null, so that the client can tell "nothing yet" from "nothing".
    expect(merge(undefined, {})).toEqual({});
  });

  it("leaves a value untouched when the diff mentions nothing", () => {
    expect(merge({ a: 1 }, {})).toEqual({ a: 1 });
  });

  it("reads a multi-element array as a literal value", () => {
    // A current server never emits one: every replacement is wrapped, so a diff
    // node is at most a 1-element array. Kept because reading it literally is
    // the only interpretation that can be right -- taking element 0, what the
    // reference client did, turns a list into its first element.
    expect(merge({ a: 1 }, { b: ["x", "y"] })).toEqual({ a: 1, b: ["x", "y"] });
  });
});

describe("immutability and sharing", () => {
  it("does not mutate the previous value", () => {
    const previous = { a: 1, list: ["x", "y"] };
    const snapshot = JSON.parse(JSON.stringify(previous)) as unknown;

    merge(previous, { a: 2, list: { $: [1, 0] } });

    expect(previous).toEqual(snapshot);
  });

  it("does not mutate the incoming diff", () => {
    // The reference implementation deleted "$" from the message to keep it out
    // of its own key sweep, which mutates data the caller may still need.
    const diff = { $: [1, 0], "1": { n: 2 } };

    merge([{ n: 1 }, { n: 2 }], diff);

    expect(diff).toEqual({ $: [1, 0], "1": { n: 2 } });
  });

  it("does not freeze or adopt a replacement out of the incoming diff", () => {
    // Freezing where the value lies is cheaper, and it is still a mutation of
    // the caller's message; keeping the value is cheaper still, and it leaves
    // the accumulated payload aliasing something the caller can edit.
    const replacement = { list: [{ n: 1 }] };
    const diff = { a: [replacement] };

    const merged = merge({ a: 0 }, diff) as { a: { list: { n: number }[] } };

    expect(Object.isFrozen(replacement)).toBe(false);
    expect(Object.isFrozen(replacement.list[0])).toBe(false);
    expect(merged.a).not.toBe(replacement);
    expect(merged.a.list[0]).not.toBe(replacement.list[0]);
    expect(Object.isFrozen(merged.a.list[0])).toBe(true);
    expect(merged.a).toEqual(replacement);
  });

  it("does not read a previous value off the prototype chain", () => {
    // "__proto__" is not an own field of the value being merged into, but it
    // reads back as Object.prototype -- an object, which is enough to send an
    // array diff down the field-by-field path and lose it.
    const diff = JSON.parse('{"__proto__": {"$": [-1], "0": "a"}}') as JsonValue;

    const merged = merge({}, diff) as Record<string, unknown>;

    expect(merged["__proto__"]).toEqual(["a"]);
  });

  it("shares subtrees the diff did not touch", () => {
    const previous = { kept: { deep: [1, 2, 3] }, changed: 1 };
    const merged = merge(previous, { changed: 2 }) as {
      kept: unknown;
      changed: number;
    };

    // Not merely equal: the same object. This is what keeps a one-field change
    // on a large payload cheap.
    expect(merged.kept).toBe(previous.kept);
    expect(merged.changed).toBe(2);
  });

  it("freezes what it produces", () => {
    const merged = merge({ a: 1 }, { a: 2 });
    expect(Object.isFrozen(merged)).toBe(true);
  });

  it("keeps a payload out of the prototype chain", () => {
    // An object literal would set the prototype here, so the diff has to be
    // built the way one actually arrives: parsed from the wire.
    const diff = JSON.parse('{"__proto__": {"polluted": true}}') as JsonValue;

    const merged = merge({ a: 1 }, diff) as Record<string, unknown>;

    expect(({} as Record<string, unknown>)["polluted"]).toBeUndefined();
    expect(Object.getPrototypeOf(merged)).toBe(Object.prototype);
  });
});

describe("applying a sequence of diffs", () => {
  it("accumulates a live query the way a subscription does", () => {
    // Frame 1 is always a full snapshot wrapped as a replacement, because the
    // server has nothing to diff against yet.
    let payload: MergeValue = merge(undefined, [
      { users: [{ name: "bob" }, { name: "alice" }] },
    ]);
    expect(payload).toEqual({ users: [{ name: "bob" }, { name: "alice" }] });

    // Frame 2: alice is renamed.
    payload = merge(payload, { users: { "1": { name: "alex" } } });
    expect(payload).toEqual({ users: [{ name: "bob" }, { name: "alex" }] });

    // Frame 3: carol is appended.
    payload = merge(payload, {
      users: { $: [[0, 2], -1], "2": [{ name: "carol" }] },
    });
    expect(payload).toEqual({
      users: [{ name: "bob" }, { name: "alex" }, { name: "carol" }],
    });

    // Frame 4: bob is dropped and the rest shift up.
    payload = merge(payload, { users: { $: [[1, 2]] } });
    expect(payload).toEqual({
      users: [{ name: "alex" }, { name: "carol" }],
    });
  });

  it("starts over from a snapshot after a reconnect", () => {
    const beforeDrop = merge(undefined, [{ users: [{ name: "bob" }] }]);
    expect(beforeDrop).toEqual({ users: [{ name: "bob" }] });

    // The socket dropped and the subscription restarted, so the server's
    // previous value is gone and it sends a full snapshot again. Merging that
    // onto the old value has to discard it rather than combine with it.
    const afterReconnect = merge(undefined, [{ users: [{ name: "zoe" }] }]);
    expect(afterReconnect).toEqual({ users: [{ name: "zoe" }] });
  });
});

describe("stripKeys", () => {
  it("removes __key recursively", () => {
    expect(
      stripKeys({
        __key: "foo",
        arr: ["x", "y", "z", { __key: "bar", q: "w" }],
      }),
    ).toEqual({ arr: ["x", "y", "z", { q: "w" }] });
  });

  it("copies, so the merge state keeps its keys for the next diff", () => {
    const payload = merge(undefined, [{ __key: 1, a: { __key: 2, b: 3 } }]);

    const stripped = stripKeys(payload) as Record<string, unknown>;

    expect(stripped).toEqual({ a: { b: 3 } });
    expect(payload).toEqual({ __key: 1, a: { __key: 2, b: 3 } });
    expect(stripped["a"]).not.toBe((payload as Record<string, unknown>)["a"]);
  });

  it("leaves everything else alone", () => {
    expect(stripKeys({ a: [1, "two", true, null] })).toEqual({
      a: [1, "two", true, null],
    });
  });

  it("renders an unfilled array slot as null", () => {
    // undefined is not JSON and Relay would not know what to do with it.
    expect(stripKeys([undefined, 1])).toEqual([null, 1]);
  });
});
