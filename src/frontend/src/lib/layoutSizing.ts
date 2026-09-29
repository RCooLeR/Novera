export const MIN_EDITOR_WIDTH = 320;
export const MIN_SIDEBAR_WIDTH = 180;
export const MIN_ASSISTANT_WIDTH = 260;
const ACTIVITY_BAR_WIDTH = 48;

const clamp = (value: number, min: number, max: number) => Math.min(Math.max(value, min), max);

export interface PanelLayout {
  showSidebar: boolean;
  showAssistant: boolean;
  sidebarWidth: number;
  assistantWidth: number;
}

/** Keep the editor usable at every viewport width, collapsing the assistant first. */
export function fitPanelLayout(
  viewportWidth: number,
  sidebarRequested: boolean,
  assistantRequested: boolean,
  sidebarWidth: number,
  assistantWidth: number,
): PanelLayout {
  const panelBudget = Math.max(0, viewportWidth - ACTIVITY_BAR_WIDTH - MIN_EDITOR_WIDTH);
  const showSidebar = sidebarRequested && panelBudget >= MIN_SIDEBAR_WIDTH;
  let showAssistant = assistantRequested && panelBudget >= MIN_ASSISTANT_WIDTH;

  if (showSidebar && showAssistant && panelBudget < MIN_SIDEBAR_WIDTH + MIN_ASSISTANT_WIDTH) {
    showAssistant = false;
  }

  if (showSidebar && showAssistant) {
    const fittedSidebar = clamp(sidebarWidth, MIN_SIDEBAR_WIDTH, panelBudget - MIN_ASSISTANT_WIDTH);
    return {
      showSidebar,
      showAssistant,
      sidebarWidth: fittedSidebar,
      assistantWidth: clamp(assistantWidth, MIN_ASSISTANT_WIDTH, panelBudget - fittedSidebar),
    };
  }
  if (showSidebar) {
    return {
      showSidebar,
      showAssistant: false,
      sidebarWidth: clamp(sidebarWidth, MIN_SIDEBAR_WIDTH, panelBudget),
      assistantWidth: 0,
    };
  }
  if (showAssistant) {
    return {
      showSidebar: false,
      showAssistant,
      sidebarWidth: 0,
      assistantWidth: clamp(assistantWidth, MIN_ASSISTANT_WIDTH, panelBudget),
    };
  }
  return { showSidebar: false, showAssistant: false, sidebarWidth: 0, assistantWidth: 0 };
}
