import { describe, expect, it } from "vitest";
import { SerialTaskQueue } from "./serialTaskQueue";

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

describe("SerialTaskQueue", () => {
  it("does not start a newer mutation until the previous one settles", async () => {
    const queue = new SerialTaskQueue();
    const first = deferred<void>();
    const order: string[] = [];
    const one = queue.run(async () => {
      order.push("first:start");
      await first.promise;
      order.push("first:end");
    });
    const two = queue.run(async () => {
      order.push("second");
    });

    await Promise.resolve();
    expect(order).toEqual(["first:start"]);
    first.resolve();
    await Promise.all([one, two]);
    expect(order).toEqual(["first:start", "first:end", "second"]);
  });

  it("continues after a rejected mutation and drain waits for the successor", async () => {
    const queue = new SerialTaskQueue();
    const failed = queue.run(async () => {
      throw new Error("stage failed");
    });
    const succeeded = queue.run(async () => "latest");

    await expect(failed).rejects.toThrow("stage failed");
    await expect(succeeded).resolves.toBe("latest");
    await expect(queue.drain()).resolves.toBeUndefined();
  });
});
