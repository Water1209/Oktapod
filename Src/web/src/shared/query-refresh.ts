export function backgroundInboxRefetchInterval(data: { items?: unknown[] } | undefined) {
  return data?.items?.length ? 5_000 : 15_000
}
