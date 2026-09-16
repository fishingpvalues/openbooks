import {
  ActionIcon,
  Badge,
  Button,
  Center,
  Collapse,
  Group,
  Loader,
  Menu,
  NumberInput,
  SegmentedControl,
  Stack,
  Switch,
  Text,
  TextInput,
  Tooltip
} from "@mantine/core";
import { showNotification } from "@mantine/notifications";
import { AnimatePresence, motion } from "motion/react";
import { CaretDown, SlidersHorizontal, Timer, Trash } from "phosphor-react";
import { FormEvent, useState } from "react";
import {
  useAddWantedMutation,
  useDeleteWantedMutation,
  useGetWantedQuery,
  type QualityFilters,
  type WantedItem
} from "../../state/api";
import { addHistoryItem } from "../../state/historySlice";
import {
  sendSearch,
  setActiveItem
} from "../../state/stateSlice";
import { useAppDispatch } from "../../state/store";
import { defaultAnimation } from "../../utils/animation";

// The wanted watchlist (v5.2.0) with the v5.3.0 lifecycle state: the
// server re-searches each entry on a schedule (backoff 5m->24h, stale
// at 7 days) and matches persist across restarts. The UI is the
// operator's view of that queue.
export default function Wanted() {
  const { data, isLoading, isError } = useGetWantedQuery(null);
  const [query, setQuery] = useState("");

  if (isLoading) {
    return (
      <Center>
        <Loader />
      </Center>
    );
  }

  if (isError) {
    return (
      <Center>
        <Text color="dimmed" size="sm">
          Wanted list unavailable.
        </Text>
      </Center>
    );
  }

  return (
    <Stack gap="xs">
      <AddWantedForm query={query} setQuery={setQuery} />
      {data && data.length > 0 ? (
        <AnimatePresence mode="popLayout">
          {data.map((item) => (
            <motion.div {...defaultAnimation} key={item.query}>
              <WantedCard key={item.query} item={item} />
            </motion.div>
          ))}
        </AnimatePresence>
      ) : (
        <Center>
          <Text color="dimmed" size="sm">
            Nothing wanted yet.
          </Text>
        </Center>
      )}
    </Stack>
  );
}

// Two verbs on one form: "Watch" adds to the wanted queue (the server
// re-searches until it matches), "Search" is the existing one-shot IRC
// search. The split is deliberate - the two paths have very different
// lifetimes and the operator must not mix them up.
function AddWantedForm({
  query,
  setQuery
}: {
  query: string;
  setQuery: (q: string) => void;
}) {
  const [autoFetch, setAutoFetch] = useState(false);
  const [withSidecar, setWithSidecar] = useState(false);
  // Quality filters (v5.3.0): persisted on the entry, so the poller
  // re-searches the SAME shape. Empty = no filter.
  const [formats, setFormats] = useState("");
  const [language, setLanguage] = useState("");
  const [maxSizeMb, setMaxSizeMb] = useState<string | number>("");
  const [prefer, setPrefer] = useState<"" | "ebook" | "audiobook">("");
  const [addWanted, { isLoading: adding }] = useAddWantedMutation();
  const dispatch = useAppDispatch();

  const buildFilters = (): QualityFilters => {
    const f: QualityFilters = {};
    const fmts = formats
      .split(",")
      .map((s) => s.trim().toLowerCase().replace(/^\./, ""))
      .filter(Boolean);
    if (fmts.length) f.formats = fmts;
    if (language.trim()) f.language = language.trim().toLowerCase();
    const mb = Number(maxSizeMb);
    if (maxSizeMb !== "" && Number.isFinite(mb) && mb > 0) {
      f.maxSizeBytes = Math.round(mb * 1024 * 1024);
    }
    if (prefer) f.prefer = prefer;
    return f;
  };

  const submit = (watch: boolean) => (event: FormEvent) => {
    event.preventDefault();
    const q = query.trim();
    if (!q) return;
    if (watch) {
      const filters = buildFilters();
      addWanted({
        query: q,
        autoFetch,
        withSidecar,
        filters
      }).then(
        () => {
          showNotification({
            title: "Watching",
            message: `${q} - the server re-searches until it matches.`,
            color: "brand"
          });
        },
        (err: any) => {
          showNotification({
            title: "Could not add",
            message: err?.data?.error ?? "the server refused the entry",
            color: "red"
          });
        }
      );
    } else {
      const timestamp = new Date().getTime();
      dispatch(addHistoryItem({ query: q, timestamp }));
      dispatch(setActiveItem({ query: q, timestamp }));
      dispatch(sendSearch(q));
    }
    setQuery("");
  };

  return (
    <form onSubmit={submit(true)}>
      <Stack gap="xs">
        <TextInput
          size="xs"
          radius="sm"
          placeholder="Title (and author, optional)"
          value={query}
          onChange={(e) => setQuery(e.currentTarget.value)}
        />
        <FilterForm
          formats={formats}
          setFormats={setFormats}
          language={language}
          setLanguage={setLanguage}
          maxSizeMb={maxSizeMb}
          setMaxSizeMb={setMaxSizeMb}
          prefer={prefer}
          setPrefer={setPrefer}
        />
        <Group gap="sm" wrap="nowrap" style={{ fontSize: 11 }}>
          <Switch
            size="xs"
            label="Auto-fetch"
            checked={autoFetch}
            onChange={(e) => setAutoFetch(e.currentTarget.checked)}
          />
          <Switch
            size="xs"
            label="Sidecar"
            checked={withSidecar}
            onChange={(e) => setWithSidecar(e.currentTarget.checked)}
          />
        </Group>
        <Group gap="xs" wrap="nowrap">
          <Button
            size="xs"
            radius="sm"
            color="brand"
            loading={adding}
            type="submit"
            w="50%">
            Watch
          </Button>
          <Button
            size="xs"
            radius="sm"
            variant="outline"
            type="button"
            w="50%"
            onClick={submit(false)}>
            Search
          </Button>
        </Group>
      </Stack>
    </form>
  );
}

// The v5.3.0 quality controls for a wanted entry. Collapsed by default:
// most entries are a plain title, and the form stays one glance tall.
// An explicit formats list is a DEMAND (a result whose format is unknown
// is dropped), so the placeholder spells out the contract.
function FilterForm({
  formats,
  setFormats,
  language,
  setLanguage,
  maxSizeMb,
  setMaxSizeMb,
  prefer,
  setPrefer
}: {
  formats: string;
  setFormats: (v: string) => void;
  language: string;
  setLanguage: (v: string) => void;
  maxSizeMb: string | number;
  setMaxSizeMb: (v: string | number) => void;
  prefer: "" | "ebook" | "audiobook";
  setPrefer: (v: "" | "ebook" | "audiobook") => void;
}) {
  const [open, setOpen] = useState(false);
  const hasFilters =
    formats.trim() !== "" ||
    language.trim() !== "" ||
    maxSizeMb !== "" ||
    prefer !== "";

  return (
    <Stack gap="xs">
      <Button
        size="xs"
        radius="sm"
        variant={hasFilters ? "light" : "subtle"}
        color="brand"
        w="100%"
        leftSection={<SlidersHorizontal size={14} />}
        rightSection={<CaretDown size={14} style={{ transform: open ? "rotate(180deg)" : undefined }} />}
        onClick={() => setOpen(!open)}>
        {hasFilters ? "Filters (set)" : "Filters"}
      </Button>
      <Collapse expanded={open}>
        <Stack gap="xs">
        <TextInput
          size="xs"
          radius="sm"
          placeholder="Formats: epub, pdf (comma list)"
          value={formats}
          onChange={(e) => setFormats(e.currentTarget.value)}
        />
        <TextInput
          size="xs"
          radius="sm"
          placeholder="Language: german, english (title match)"
          value={language}
          onChange={(e) => setLanguage(e.currentTarget.value)}
        />
        <NumberInput
          size="xs"
          radius="sm"
          placeholder="Max size (MB)"
          value={maxSizeMb}
          onChange={setMaxSizeMb}
          min={1}
          step={1}
          decimalScale={0}
        />
        <SegmentedControl
          size="xs"
          radius="sm"
          data={[
            { label: "Any", value: "" },
            { label: "eBook", value: "ebook" },
            { label: "Audiobook", value: "audiobook" }
          ]}
          value={prefer}
          onChange={(v) => setPrefer(v as "" | "ebook" | "audiobook")}
        />
        </Stack>
      </Collapse>
    </Stack>
  );
}

// The v5.3.0 quality filters in human-readable form for the details menu:
// "epub, pdf · german · ≤ 50 MB · ebook".
function formatFilters(f: QualityFilters): string {
  const parts: string[] = [];
  if (f.formats?.length) parts.push(f.formats.join(", "));
  if (f.language) parts.push(f.language);
  if (f.maxSizeBytes) parts.push(`≤ ${Math.round(f.maxSizeBytes / 1024 / 1024)} MB`);
  if (f.prefer) parts.push(f.prefer);
  return parts.join(" · ");
}

interface WantedCardProps {
  item: WantedItem;
}

function WantedCard({ item }: WantedCardProps) {
  const [deleteWanted] = useDeleteWantedMutation();
  const matched = !!item.matchedAt;
  const stale = !!item.staleSince;

  // v5.4.3: a visible control, not only the context menu. The menu opens on a
  // real pointer sequence only (a scripted or headless click cannot reach it),
  // and burying the one destructive action two clicks deep was unfriendly
  // anyway.
  const stopWatching = () =>
    deleteWanted(item.query).then(() =>
      showNotification({
        title: "Removed from watch",
        message: item.query,
        color: "yellow"
      })
    );

  return (
    <Group gap={4} wrap="nowrap">
    <Menu shadow="md">
      <Menu.Target>
        <Tooltip
          label={`${item.query}${item.author ? ` - ${item.author}` : ""}`}
          openDelay={1_000}>
          <button
            className="wanted-card-target"
            style={{
              display: "flex",
              alignItems: "center",
              gap: 6,
              width: "100%",
              cursor: "pointer",
              border: "none",
              background: "none",
              padding: 0,
              textAlign: "start"
            }}>
            <Badge
              color={matched ? "green" : stale ? "yellow" : "brand"}
              radius="sm"
              size="sm"
              variant="light">
              {matched ? "matched" : stale ? "stale" : "watching"}
            </Badge>
            <Text
              size="sm"
              lineClamp={1}
              style={{
                flex: 1,
                overflow: "hidden",
                textOverflow: "ellipsis",
                whiteSpace: "nowrap"
              }}>
              {item.query}
            </Text>
          </button>
        </Tooltip>
      </Menu.Target>

      <Menu.Dropdown>
        <Menu.Item disabled leftSection={<Timer size={16} />}>
          <div>
            <Text size="xs">
              Added {new Date(item.addedAt).toLocaleDateString("en-US")}
              {item.matchedAt
                ? `, matched ${new Date(item.matchedAt).toLocaleDateString(
                    "en-US"
                  )}`
                : ""}
            </Text>
            {item.nextAttemptAt && (
              <Text size="xs" c="dimmed">
                Next poll{" "}
                {new Date(item.nextAttemptAt).toLocaleString("en-US")}
              </Text>
            )}
            {item.attempts > 0 && (
              <Text size="xs" c="dimmed">
                {item.attempts} rounds
                {item.failedRounds
                  ? `, ${item.failedRounds} consecutive no-match`
                  : ""}
                {item.seenReleases
                  ? `, ${Object.keys(item.seenReleases).length} releases seen`
                  : ""}
              </Text>
            )}
            {(item.autoFetch || item.withSidecar) && (
              <Text size="xs" c="dimmed">
                {[item.autoFetch && "auto-fetch", item.withSidecar && "sidecar"]
                  .filter(Boolean)
                  .join(" + ")}
              </Text>
            )}
            {item.filters && formatFilters(item.filters) !== "" && (
              <Text size="xs" c="dimmed">
                {formatFilters(item.filters)}
              </Text>
            )}
          </div>
        </Menu.Item>

        <Menu.Item
          color="red"
          leftSection={<Trash size={16} weight="bold" />}
          onClick={stopWatching}>
          Stop watching
        </Menu.Item>
      </Menu.Dropdown>
    </Menu>

      <Tooltip label="Stop watching">
        <ActionIcon
          variant="subtle"
          color="red"
          aria-label={`Stop watching ${item.query}`}
          onClick={stopWatching}>
          <Trash size={16} weight="bold" />
        </ActionIcon>
      </Tooltip>
    </Group>
  );
}
