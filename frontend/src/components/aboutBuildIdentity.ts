export type BuildIdentity = {
  productName: string;
  version: string;
  commit: string;
  buildDate: string;
  channel: string;
  dirty: boolean;
  goVersion: string;
  wailsVersion: string;
};

export function buildIdentityText(info: BuildIdentity): string {
  const dirty = info.dirty ? ", locally modified" : "";
  return `${info.productName} ${info.version} · ${info.channel} · commit ${info.commit}${dirty} · built ${info.buildDate}`;
}
