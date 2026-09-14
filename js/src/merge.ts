/**
 * Applies lightning's JSON diffs to a previously received payload.
 *
 * The server does not re-send a payload when a live query's data changes: it
 * re-executes the query, diffs the new result against the one it last sent on
 * this subscription, and writes only the difference. Holding the accumulated
 * value and replaying every diff onto it is the client's half of that contract.
 *
 * A diff node is one of four things, and the encodings are distinguishable by
 * JSON type alone (see diff/diff.go):
 *
 *   | node                   | meaning                                        |
 *   | ---------------------- | ---------------------------------------------- |
 *   | `[]` (0 elements)      | the field was deleted                          |
 *   | `[v]` (1 element)      | the field was replaced by the complex value v   |
 *   | a non-object (`"a"`)   | the field was replaced by that scalar           |
 *   | an object              | recurse: an array diff or a field-by-field diff |
 *
 * A replacement is always wrapped in an array because that is what makes it
 * distinguishable from a recursive diff; the wrapping is also what tells the
 * client "stop, this subtree is already final" rather than "merge into me".
 *
 * An array diff is an object whose numeric keys index the array *after*
 * reordering, plus an optional "$" holding the reordering itself: for each
 * slot of the new array, where to find its previous value. Runs of consecutive
 * source indices are compressed to `[start, length]`, and -1 means "this slot
 * has no previous value" (a replacement for it follows under its index key).
 */

/** A JSON scalar. */
export type JsonScalar = string | number | boolean | null;

/** Any value that can be transported as JSON. */
export type JsonValue = JsonScalar | JsonValue[] | { [key: string]: JsonValue };

/** A JSON object. */
export type JsonObject = { [key: string]: JsonValue };

/**
 * An accumulated payload.
 *
 * This is JSON plus `undefined`, which appears in two places: as the seed value
 * before the first payload of a subscription has arrived, and transiently in an
 * array slot that a reordering marked as having no previous value. A correct
 * server always fills such a slot in the same diff.
 */
export type MergeValue =
  | JsonScalar
  | undefined
  | MergeValue[]
  | { [key: string]: MergeValue };

/**
 * The field the server adds to objects of a keyed type so that it can line up
 * array elements across executions. It is a server-side correlation token, not
 * part of any selection set.
 */
export const KEY_FIELD = "__key";

/** The reordering field of an array diff. */
const REORDER_KEY = "$";

/**
 * Combines a diff from the server with the value it was computed against.
 *
 * `original` is the accumulated payload (`undefined` before the first one), and
 * `update` is the `message` of an `update` or `result` envelope. The result is
 * a new frozen value. Neither argument is mutated -- not even by being frozen
 * -- and unchanged subtrees are shared with `original` rather than copied,
 * which is what keeps a small diff on a long list cheap to apply. Nothing is
 * ever shared with `update`, which stays the caller's to do as it likes with.
 */
export function merge(original: MergeValue, update: JsonValue): MergeValue {
  if (Array.isArray(update)) {
    return mergeReplacement(update);
  }

  if (update === null || typeof update !== "object") {
    // Scalar replacement. Note that a field *becoming* null is encoded as the
    // 1-element array [null] and handled above, so the differ never writes a
    // bare null into a diff at all: one reaches here only as the whole message
    // of an envelope that carried one, where reading it literally is right
    // anyway.
    return update;
  }

  // `update` is a diff object, so it is either an array diff or a recursive
  // field-by-field diff, and the previous value decides which.
  if (Array.isArray(original)) {
    return mergeArray(original, update);
  }

  // An object previous value decides it too, and has to be allowed to: the
  // server computes a reordering only for an array, so a "$" arriving against
  // an object is an ordinary field with that name. A selection set cannot
  // produce one -- GraphQL names have no "$" -- but a JSON-valued custom scalar
  // can, and diffMap will happily emit {"$": 2} for it. Reading that as a
  // reordering throws the whole object away.
  if (isPlainObject(original)) {
    return mergeMap(original, update);
  }

  // No previous value to go on, so "$" is the only signal left. A current
  // server does not get here: a field that has only just appeared is wrapped as
  // a replacement, and so never arrives as a bare diff object. An array slot a
  // reordering left unfilled can still be undefined here.
  if (hasOwn(update, REORDER_KEY)) {
    return mergeArray([], update);
  }

  return mergeMap(original, update);
}

/**
 * Handles an array-shaped diff node: a replacement or a deletion.
 */
function mergeReplacement(update: JsonValue[]): MergeValue {
  if (update.length === 1) {
    // Complex replacement. The value is already fully materialized by the
    // server, so it replaces the previous subtree outright -- recursing into it
    // would be wrong, not merely wasteful.
    return copyValue(update[0]);
  }

  if (update.length === 0) {
    // A deletion, which only means anything to the object that holds the field.
    // mergeMap intercepts it before recursing, so arriving here means the
    // server encoded a deletion somewhere it cannot be honoured. Absent is the
    // closest we can come.
    return undefined;
  }

  // Unreachable against a current server: a replacement is always wrapped in a
  // 1-element array, so a diff node is never a longer array.
  //
  // It was reachable until diff/diff.go was fixed to wrap a field that is new
  // since the last execution. Before that, a new field holding an array arrived
  // raw: a longer one landed here, and -- worse, because they were silent --
  // one holding [v] was unwrapped to v and one holding [] was read as a
  // deletion. Only the first of those three could be repaired on this side, so
  // the server was fixed; reading a longer array literally is kept because it
  // is the only interpretation that can be right, and it cannot misfire against
  // a server that wraps properly.
  return copyValue(update);
}

/**
 * Copies a complete value out of a diff and into the accumulated payload,
 * freezing as it goes.
 *
 * It cannot be frozen where it lies. The value belongs to the caller's message,
 * which merge() promises not to touch, and freezing is a mutation -- a silent
 * one that a caller reusing or editing its own message would discover much
 * later and somewhere else. Nor can it be adopted as it is: the accumulated
 * payload would then alias data the caller is still free to change underneath
 * it, and every later diff would be applied to whatever it had become.
 */
function copyValue(value: MergeValue): MergeValue {
  if (Array.isArray(value)) {
    const copy: MergeValue[] = [];
    for (const item of value) {
      copy.push(copyValue(item));
    }
    Object.freeze(copy);
    return copy;
  }

  if (isPlainObject(value)) {
    const copy: { [key: string]: MergeValue } = {};
    for (const key of Object.keys(value)) {
      defineField(copy, key, copyValue(value[key]));
    }
    Object.freeze(copy);
    return copy;
  }

  return value;
}

/**
 * Rebuilds an array from a reordering plus per-slot diffs.
 */
function mergeArray(
  original: readonly MergeValue[],
  update: JsonObject,
): MergeValue {
  const merged: MergeValue[] = [];

  if (hasOwn(update, REORDER_KEY)) {
    const reorder = update[REORDER_KEY];
    if (Array.isArray(reorder)) {
      for (const entry of reorder) {
        if (Array.isArray(entry)) {
          // A run of consecutive source indices, [start, length]. The second
          // element is a COUNT, not an end index: the loop bound is exclusive.
          const start = entry[0];
          const length = entry[1];
          if (typeof start === "number" && typeof length === "number") {
            for (let i = start; i < start + length; i++) {
              merged.push(original[i]);
            }
          } else {
            // A malformed run still stands for slots, and the index keys below
            // address slots. Pushing nothing would slide every later element
            // down by the width of the run, turning one unreadable entry into a
            // wrong array; holes keep everything after it lined up.
            const slots = typeof length === "number" && length > 0 ? length : 1;
            for (let i = 0; i < slots; i++) {
              merged.push(undefined);
            }
          }
        } else if (typeof entry === "number" && entry >= 0) {
          // An out-of-range index reads as undefined. The server should never
          // emit one, but a hole is recoverable where a throw is not.
          merged.push(original[entry]);
        } else {
          // -1: this slot has no previous value. The diff carries a replacement
          // for it under its index key, applied below.
          merged.push(undefined);
        }
      }
    }
  } else {
    // An ABSENT "$" means "same length, same order" -- it does not mean "empty".
    // {"$": []} is a distinct and legal diff meaning the new array is empty, so
    // this has to test for the key's presence. (The reference client wrote
    // `update.$ || [[0, original.length]]` and got away with it only because []
    // is truthy in JavaScript.)
    for (const item of original) {
      merged.push(item);
    }
  }

  for (const key of Object.keys(update)) {
    if (key === REORDER_KEY) {
      // Skipped rather than deleted from `update`: the reference client deleted
      // the key to keep it out of this loop, mutating the caller's message.
      continue;
    }

    // Number() is far more generous than the key spelling the server emits:
    // "", " 1", "0x2" and "1e3" are all numbers to it, and any of them would
    // write to a slot nobody addressed. Requiring the key to be the canonical
    // decimal spelling of its own value is what makes this exactly strconv.Itoa.
    const index = Number(key);
    if (!Number.isInteger(index) || index < 0 || String(index) !== key) {
      continue;
    }

    // Index keys address the array *after* reordering.
    merged[index] = merge(merged[index], update[key]);
  }

  Object.freeze(merged);
  return merged;
}

/**
 * Applies a field-by-field diff to an object.
 */
function mergeMap(original: MergeValue, update: JsonObject): MergeValue {
  // Fields the diff does not mention survive by shallow copy, so untouched
  // subtrees keep their identity. Anything that is not an object starts empty:
  // either the field did not exist yet, or a scalar is being replaced by an
  // object, and in both cases there is nothing to carry over.
  const merged: Record<string, MergeValue> = isPlainObject(original)
    ? { ...original }
    : {};

  for (const key of Object.keys(update)) {
    const value = update[key];

    // Deletion is tested before recursing, because merge() returns a value and
    // so has no way to express "this field is gone".
    if (Array.isArray(value) && value.length === 0) {
      delete merged[key];
    } else {
      // Read as an own property. A diff may name a field the prototype also
      // has -- "toString", "constructor" -- and plain indexing would hand the
      // inherited function to merge() as the previous value of a field that in
      // fact has none.
      const previous = hasOwn(merged, key) ? merged[key] : undefined;
      defineField(merged, key, merge(previous, value));
    }
  }

  Object.freeze(merged);
  return merged;
}

/**
 * Returns a deep copy of a merged payload with every {@link KEY_FIELD} removed.
 *
 * Relay normalizes a response against the selection set that produced it, and
 * nothing selected `__key`. The copy is what makes this safe to call on live
 * merge state: the accumulated value keeps its keys for the next diff.
 */
export function stripKeys(value: MergeValue): JsonValue {
  if (Array.isArray(value)) {
    const out: JsonValue[] = [];
    // An explicit loop rather than .map, which preserves holes: a reordering
    // that pointed past the end of the previous array leaves them behind.
    for (let i = 0; i < value.length; i++) {
      out.push(stripKeys(value[i]));
    }
    return out;
  }

  if (isPlainObject(value)) {
    const out: JsonObject = {};
    for (const key of Object.keys(value)) {
      if (key === KEY_FIELD) {
        continue;
      }
      defineField(out, key, stripKeys(value[key]));
    }
    return out;
  }

  // undefined is not JSON. It reaches here only from an array slot that a
  // reordering left empty and no replacement filled, which a correct server
  // does not emit; null is the honest representation for a JSON consumer.
  return value === undefined ? null : value;
}

function isPlainObject(
  value: MergeValue,
): value is { [key: string]: MergeValue } {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function hasOwn(object: object, key: string): boolean {
  return Object.prototype.hasOwnProperty.call(object, key);
}

/**
 * Sets a field, without letting a payload reach the prototype chain.
 *
 * `target[key] = value` for the key "__proto__" runs the inherited setter and
 * reparents the object instead of adding a field. A GraphQL response should
 * never contain that name, which is exactly why it is worth not trusting.
 */
function defineField<T>(target: Record<string, T>, key: string, value: T): void {
  if (key === "__proto__") {
    Object.defineProperty(target, key, {
      value,
      writable: true,
      enumerable: true,
      configurable: true,
    });
  } else {
    target[key] = value;
  }
}
