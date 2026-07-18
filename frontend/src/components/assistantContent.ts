// Provider protocol data arrives in structural tool-call fields. Free-form
// content is user-visible by contract; phrase-based filtering would truncate
// legitimate prose/code and make rendering/copying disagree with the response.
export function visibleAssistantContent(content: string): string {
  return content.replace(/\r\n/g, "\n").replace(/\r/g, "\n").trim();
}
