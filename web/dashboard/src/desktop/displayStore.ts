import {
  type CodecConfigPayload,
  type DesktopDisplay,
  type DesktopStreamState,
  type VideoAccessUnitMetadata,
  validateDisplayDescriptor,
  validateStreamTransition,
} from "./protocol";

export type DisplayState = {
  inventory: DesktopDisplay[];
  selectedID: string | null;
  generation: number;
  minimumGeneration: number;
  mode: "fit" | "actual";
  codec?: string;
  width: number;
  height: number;
};

export function initialDisplayState(mode: DisplayState["mode"] = "fit"): DisplayState {
  return { inventory: [], selectedID: null, generation: 0, minimumGeneration: 0, mode, width: 0, height: 0 };
}

export function applyDisplayInventory(state: DisplayState, inventory: DesktopDisplay[]): DisplayState {
  inventory.forEach(validateDisplayDescriptor);
  if (new Set(inventory.map(display => display.id)).size !== inventory.length) throw new Error("desktop_display_invalid");
  const selectedID = state.selectedID && inventory.some(display => display.id === state.selectedID)
    ? state.selectedID
    : inventory.find(display => display.primary)?.id ?? inventory[0]?.id ?? null;
  const previousSelected = state.inventory.find(display => display.id === state.selectedID);
  const nextSelected = inventory.find(display => display.id === selectedID);
  const selectedGeometryChanged = Boolean(previousSelected && nextSelected &&
    (previousSelected.width !== nextSelected.width || previousSelected.height !== nextSelected.height || previousSelected.rotation !== nextSelected.rotation));
  if (state.selectedID !== null && (selectedID !== state.selectedID || selectedGeometryChanged)) {
    return {
      ...state,
      inventory: inventory.map(display => ({ ...display })),
      selectedID,
      generation: 0,
      minimumGeneration: Math.max(state.minimumGeneration, state.generation + 1),
      codec: undefined,
      width: 0,
      height: 0,
    };
  }
  return { ...state, inventory: inventory.map(display => ({ ...display })), selectedID };
}

export function applyCodecConfiguration(state: DisplayState, config: CodecConfigPayload): DisplayState {
  if (config.generation < state.minimumGeneration) throw new Error("desktop_stream_generation_invalid");
  const previous: DesktopStreamState | null = state.generation > 0 ? {
    generation: state.generation,
    display_id: state.selectedID || "",
    width: state.width,
    height: state.height,
    codec: state.codec,
  } : null;
  validateStreamTransition(previous, config);
  return {
    ...state,
    selectedID: config.display_id,
    generation: config.generation,
    minimumGeneration: config.generation,
    codec: config.codec,
    width: config.width,
    height: config.height,
  };
}

export function acceptAccessUnit(state: DisplayState, metadata: VideoAccessUnitMetadata): boolean {
  return state.generation > 0 && metadata.generation === state.generation;
}
