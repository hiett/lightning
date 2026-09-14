import { describe, expect, it } from "vitest";

import { parseServerEnvelope } from "./protocol.js";

/**
 * The frames below are the ones graphql/server.go's outEnvelope can produce,
 * plus the ones anything else on the network can. The rule the tests hold to is
 * that a frame is either one of the four documented envelopes or it is
 * undefined: there is no third outcome where a half-understood frame is acted
 * on.
 */
describe("parseServerEnvelope", () => {
  describe("accepts", () => {
    it("an update carrying a diff", () => {
      expect(
        parseServerEnvelope('{"id":"0","type":"update","message":{"a":1}}'),
      ).toEqual({ type: "update", id: "0", message: { a: 1 }, metadata: undefined });
    });

    it("a result carrying a wrapped payload", () => {
      expect(
        parseServerEnvelope('{"id":"7","type":"result","message":[{"a":1}]}'),
      ).toEqual({
        type: "result",
        id: "7",
        message: [{ a: 1 }],
        metadata: undefined,
      });
    });

    it("an error", () => {
      expect(
        parseServerEnvelope('{"id":"1","type":"error","message":"boom"}'),
      ).toEqual({
        type: "error",
        id: "1",
        message: "boom",
        metadata: undefined,
      });
    });

    it("the id-less echo the server replies with", () => {
      expect(parseServerEnvelope('{"type":"echo"}')).toEqual({ type: "echo" });
    });

    it("an echo that came back with its id", () => {
      // The server echoes whatever it is given, so an id on the way out comes
      // back on the way in.
      expect(parseServerEnvelope('{"id":"3","type":"echo"}')).toEqual({
        type: "echo",
      });
    });

    it("metadata alongside a message", () => {
      expect(
        parseServerEnvelope(
          '{"id":"0","type":"update","message":{},"metadata":{"trace":"abc"}}',
        ),
      ).toEqual({
        type: "update",
        id: "0",
        message: {},
        metadata: { trace: "abc" },
      });
    });

    it("an error with an empty id", () => {
      // The server copies the id of the frame that caused the error, which for
      // a frame it could not parse is "". A valid envelope that matches no
      // request is not the same thing as an invalid one.
      expect(
        parseServerEnvelope('{"id":"","type":"error","message":"bad frame"}'),
      ).toEqual({ type: "error", id: "", message: "bad frame", metadata: undefined });
    });
  });

  describe("repairs", () => {
    it("an omitted message, which the server drops when the diff is nil", () => {
      // outEnvelope tags Message `omitempty`, so "no change" arrives as an
      // absent key. Merging {} is a no-op, which is what that means.
      expect(
        parseServerEnvelope('{"id":"0","type":"update"}'),
      ).toMatchObject({ type: "update", id: "0", message: {} });
    });

    it("but keeps an explicit null, which is a scalar replacement", () => {
      expect(
        parseServerEnvelope('{"id":"0","type":"update","message":null}'),
      ).toMatchObject({ type: "update", id: "0", message: null });
    });

    it("an error whose message is not a string", () => {
      expect(
        parseServerEnvelope('{"id":"1","type":"error","message":{"a":1}}'),
      ).toMatchObject({ message: "Internal server error" });
    });

    it("metadata that is not an object", () => {
      expect(
        parseServerEnvelope(
          '{"id":"0","type":"update","message":{},"metadata":[1,2]}',
        ),
      ).toMatchObject({ metadata: undefined });
    });
  });

  describe("rejects", () => {
    it("a binary frame", () => {
      expect(parseServerEnvelope(new Uint8Array([1, 2]))).toBeUndefined();
      expect(parseServerEnvelope(null)).toBeUndefined();
      expect(parseServerEnvelope(undefined)).toBeUndefined();
    });

    it("text that is not JSON", () => {
      expect(parseServerEnvelope("not json")).toBeUndefined();
      expect(parseServerEnvelope("")).toBeUndefined();
    });

    it("JSON that is not an object", () => {
      expect(parseServerEnvelope("[]")).toBeUndefined();
      expect(parseServerEnvelope("null")).toBeUndefined();
      expect(parseServerEnvelope('"update"')).toBeUndefined();
      expect(parseServerEnvelope("4")).toBeUndefined();
    });

    it("a type it does not know", () => {
      expect(
        parseServerEnvelope('{"id":"0","type":"ping","message":{}}'),
      ).toBeUndefined();
      expect(parseServerEnvelope('{"id":"0","message":{}}')).toBeUndefined();
    });

    it("an addressed frame with no id, or an id that is not a string", () => {
      expect(parseServerEnvelope('{"type":"update","message":{}}')).toBeUndefined();
      expect(
        parseServerEnvelope('{"id":0,"type":"update","message":{}}'),
      ).toBeUndefined();
    });
  });
});
