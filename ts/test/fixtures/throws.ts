// A workflows module whose own code fails: the export shows where.
function broken(): never {
  throw new RangeError("a helper failed");
}

export const value: never = broken();
