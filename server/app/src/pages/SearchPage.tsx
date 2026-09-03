import {
  ActionIcon,
  Badge,
  Button,
  Center,
  Group,
  Image,
  Stack,
  Switch,
  Text,
  TextInput,
  Tooltip,
  Title
} from "@mantine/core";
import { showNotification } from "@mantine/notifications";
import { MagnifyingGlass, Sidebar, Warning } from "phosphor-react";
import { FormEvent, useEffect, useMemo, useState } from "react";
import image from "../assets/reading.svg";
import { createStyles } from "../mantine/createStyles";
import BookTable from "../components/tables/BookTable";
import ErrorTable from "../components/tables/ErrorTable";
import UnifiedTable from "../components/tables/UnifiedTable";
import {
  useUnifiedSearchMutation,
  type UnifiedSourceStatus,
  type UnifiedResult
} from "../state/api";
import { MessageType } from "../state/messages";
import { sendMessage, sendSearch, toggleSidebar } from "../state/stateSlice";
import { useAppDispatch, useAppSelector } from "../state/store";

const useStyles = createStyles(
  (theme, { errorMode }: { errorMode: boolean }) => ({
    stack: {
      minWidth: "100%",
      margin: theme.spacing.xl,
      backgroundColor: theme.colors.blue[0]
    },
    wFull: {
      width: "100%"
    },
    errorToggle: {
      "alignSelf": "start",
      "height": "24px",
      "marginBottom": theme.spacing.xs,
      "fontWeight": 500,
      "color":
        theme.colorScheme === "dark"
          ? errorMode
            ? theme.colors.dark[8]
            : theme.colors.dark[2]
          : errorMode
            ? theme.white
            : theme.colors.dark[3],
      "&:hover": {
        backgroundColor:
          theme.colorScheme === "dark"
            ? errorMode
              ? theme.colors.brand[3]
              : theme.colors.dark[7]
            : errorMode
              ? theme.colors.brand[5]
              : theme.colors.gray[1]
      }
    }
  })
);

export default function SearchPage() {
  const dispatch = useAppDispatch();
  const activeItem = useAppSelector((store) => store.state.activeItem);
  const opened = useAppSelector((store) => store.state.isSidebarOpen);

  const [searchQuery, setSearchQuery] = useState("");
  const [showErrors, setShowErrors] = useState(false);
  // v5.2.0: the Prowlarr leg. On = the search goes through
  // POST /api/v1/search/unified (IRC + Prowlarr in one REST call, results
  // rendered below); off = the existing one-shot IRC websocket search.
  const [withProwlarr, setWithProwlarr] = useState(true);
  const [unifiedResults, setUnifiedResults] = useState<UnifiedResult[]>([]);
  const [unifiedSources, setUnifiedSources] = useState<
    UnifiedSourceStatus[] | null
  >(null);
  const [unifiedTook, setUnifiedTook] = useState<string | null>(null);
  const [unifiedActive, setUnifiedActive] = useState(false);
  const [unifiedError, setUnifiedError] = useState<string | null>(null);
  const [unifiedSearch, { isLoading: unifiedLoading }] =
    useUnifiedSearchMutation();

  const hasErrors = (activeItem?.errors ?? []).length > 0;
  const errorMode = showErrors && activeItem;
  const validInput = errorMode
    ? searchQuery.startsWith("!")
    : searchQuery !== "";

  const { classes, theme } = useStyles({ errorMode: !!errorMode });

  useEffect(() => {
    setShowErrors(false);
  }, [activeItem]);

  const clearUnified = () => {
    setUnifiedResults([]);
    setUnifiedSources(null);
    setUnifiedTook(null);
    setUnifiedError(null);
    setUnifiedActive(false);
  };

  const searchHandler = (event: FormEvent) => {
    event.preventDefault();

    if (errorMode) {
      clearUnified();
      dispatch(
        sendMessage({
          type: MessageType.DOWNLOAD,
          payload: { book: searchQuery }
        })
      );
    } else if (withProwlarr) {
      clearUnified();
      setUnifiedActive(true);
      unifiedSearch({ query: searchQuery }).then(
        (res) => {
          const r = res.data;
          if (!r) return;
          setUnifiedResults(r.results);
          setUnifiedSources(r.sources);
          setUnifiedTook(r.took);
        },
        (err: any) => {
          setUnifiedError(
            (err?.data?.error ?? `HTTP ${err?.status ?? "error"}`) +
              (err?.status === 429 ? " - rate limit, retry shortly" : "")
          );
        }
      );
    } else {
      clearUnified();
      dispatch(sendSearch(searchQuery));
    }

    setSearchQuery("");
  };

  const bookTable = useMemo(
    () => <BookTable books={activeItem?.results ?? []} />,
    [activeItem?.results]
  );

  const errorTable = useMemo(
    () => (
      <ErrorTable
        errors={activeItem?.errors ?? []}
        setSearchQuery={setSearchQuery}
      />
    ),
    [activeItem?.errors]
  );

  return (
    <Stack
      gap={0}
      align="center"
      style={{ width: "100%", margin: theme.spacing.xl }}>
      <form className={classes.wFull} onSubmit={(e) => searchHandler(e)}>
        <Group
          wrap="nowrap"
          gap="md"
          style={{ marginBottom: theme.spacing.md }}>
          {!opened && (
            <ActionIcon size="lg" onClick={() => dispatch(toggleSidebar())}>
              <Sidebar weight="bold" size={20}></Sidebar>
            </ActionIcon>
          )}
          <TextInput
            className={classes.wFull}
            variant="filled"
            disabled={
              (activeItem !== null && !activeItem.results) || unifiedLoading
            }
            value={searchQuery}
            onChange={(e: any) => setSearchQuery(e.target.value)}
            placeholder={
              errorMode ? "Download a book manually." : "Search for a book."
            }
            radius="md"
            type="search"
            leftSection={<MagnifyingGlass weight="bold" size={22} />}
            required
          />

          <Tooltip label="Also search Prowlarr (usenet indexers)">
            <Switch
              checked={withProwlarr}
              onChange={(e) => setWithProwlarr(e.currentTarget.checked)}
              label="+ Prowlarr"
              size="sm"
              style={{ whiteSpace: "nowrap" }}
            />
          </Tooltip>

          <Button
            type="submit"
            color={theme.colorScheme === "dark" ? "brand.2" : "brand"}
            disabled={!validInput || unifiedLoading}
            loading={unifiedLoading}
            radius="md"
            variant={validInput ? "gradient" : "default"}
            gradient={{ from: "brand.4", to: "brand.3" }}>
            {errorMode ? "Download" : "Search"}
          </Button>
        </Group>
      </form>

      {hasErrors && (
        <Button
          className={classes.errorToggle}
          variant={errorMode ? "filled" : "subtle"}
          onClick={() => setShowErrors((show) => !show)}
          leftSection={<Warning size={18} />}
          size="xs">
          {activeItem?.errors?.length} Parsing{" "}
          {activeItem?.errors?.length === 1 ? "Error" : "Errors"}
        </Button>
      )}

      {unifiedSources && (
        <Group gap="sm" style={{ marginBottom: theme.spacing.sm }}>
          {unifiedSources.map((s) => (
            <Tooltip
              key={s.source}
              label={s.note ? `${s.status}: ${s.note}` : s.status}>
              <Badge
                color={
                  s.status === "ok"
                    ? "green"
                    : s.status === "rate-limited" || s.status === "busy"
                      ? "yellow"
                      : s.status === "not-configured"
                        ? "gray"
                        : "red"
                }
                variant="light"
                size="sm">
                {s.source} {s.status}
                {s.hits > 0 ? ` (${s.hits})` : ""}
              </Badge>
            </Tooltip>
          ))}
          {unifiedTook && (
            <Text size="xs" c="dimmed">
              {unifiedTook}
            </Text>
          )}
        </Group>
      )}

      {unifiedError && (
        <Text size="sm" c="red" style={{ marginBottom: theme.spacing.sm }}>
          {unifiedError}
        </Text>
      )}

      {unifiedActive ? (
        <UnifiedTable results={unifiedResults} />
      ) : !activeItem ? (
        <Center style={{ height: "100%", width: "100%" }}>
          <Stack align="center">
            <Title fw="normal" ta="center">
              Search a book to get started.
            </Title>
            <Image
              width={600}
              fit="contain"
              src={image}
              alt="person reading"
              visibleFrom="md"
            />
            <Image
              width={300}
              fit="contain"
              src={image}
              alt="person reading"
              hiddenFrom="md"
            />
          </Stack>
        </Center>
      ) : errorMode ? (
        errorTable
      ) : (
        bookTable
      )}
    </Stack>
  );
}
