export interface BigFileEditState {
  editCount: number;
}

/**
 * Keeps edit entry atomic from the viewer's perspective: preparation and the
 * first bounded edit window must both succeed before React state is committed.
 */
export async function prepareBigFileEditEntry<State extends BigFileEditState, Window>(
  prepare: () => Promise<State>,
  loadWindow: () => Promise<Window>,
  commit: (state: State, window: Window) => void,
  releaseClean: () => Promise<unknown>,
): Promise<void> {
  let prepared: State | null = null;
  try {
    prepared = await prepare();
    const window = await loadWindow();
    commit(prepared, window);
  } catch (error) {
    if (prepared?.editCount === 0) {
      try {
        await releaseClean();
      } catch {
        // If it became dirty concurrently, the backend refusal preserves it.
      }
    }
    throw error;
  }
}

/**
 * Releases the expensive prepared fingerprint only after a clean edit-mode
 * exit. Dirty state remains installed for save/discard actions.
 */
export async function releaseBigFileEditIfClean<State extends BigFileEditState>(
  state: State | null,
  releaseClean: () => Promise<State>,
): Promise<State | null> {
  if (state === null || state.editCount !== 0) return null;
  return releaseClean();
}
