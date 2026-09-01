import {
  AppShell,
  Box,
  Button,
  MantineColorScheme,
  MantineProvider,
  PasswordInput,
  Stack,
  Text,
  Title
} from "@mantine/core";
import { Notifications } from "@mantine/notifications";
import { FormEvent, ReactNode, useState } from "react";
import { createStyles } from "./mantine/createStyles";
import { mantineStylesTransform } from "./mantine/stylesTransform";
import NotificationDrawer from "./components/drawer/NotificationDrawer";
import Sidebar from "./components/sidebar/Sidebar";
import SearchPage from "./pages/SearchPage";
import { useGetHealthQuery } from "./state/api";
import { getToken, setToken } from "./state/token";
import { useAppSelector } from "./state/store";

const useStyles = createStyles((theme) => ({
  wrapper: {
    boxSizing: "border-box",
    display: "flex",
    flexWrap: "nowrap",
    maxHeight: "100vh",
    minHeight: "100vh",
    backgroundColor:
      theme.colorScheme === "dark" ? theme.colors.dark[8] : theme.colors.gray[0]
  }
}));

// Full-screen gate for the token flow. Each gate renders its own
// MantineProvider so Mantine components style correctly outside the
// main AppInner provider tree.
function GateProvider({ children }: { children: ReactNode }) {
  const { classes } = useStyles();
  return (
    <MantineProvider
      colorSchemeManager={{
        get: (defaultValue: MantineColorScheme) =>
          (localStorage.getItem("color-scheme") as MantineColorScheme) ||
          defaultValue,
        set: (value: MantineColorScheme) =>
          localStorage.setItem("color-scheme", value),
        subscribe: () => {},
        unsubscribe: () => {},
        clear: () => localStorage.removeItem("color-scheme")
      }}
      theme={{
        primaryColor: "brand",
        primaryShade: { light: 4, dark: 2 },
        colors: {
          brand: [
            "#e0ecff",
            "#b0c6ff",
            "#7e9fff",
            "#4c79ff",
            "#3366ff",
            "#0039e6",
            "#002db4",
            "#002082",
            "#001351",
            "#000621"
          ]
        }
      }}
      stylesTransform={mantineStylesTransform}>
      <Box
        className={classes.wrapper}
        style={{
          alignItems: "center",
          justifyContent: "center",
          display: "flex"
        }}>
        {children}
      </Box>
    </MantineProvider>
  );
}

// Token entry form shared by the "no token yet" and "token rejected"
// gates. Submitting stores the token and reloads the page.
function TokenForm({
  heading,
  body,
  onSubmit
}: {
  heading: string;
  body: string;
  onSubmit: (token: string) => void;
}) {
  const [value, setValue] = useState("");
  const handleSubmit = (event: FormEvent) => {
    event.preventDefault();
    onSubmit(value);
  };

  return (
    <Stack align="center" gap="sm" style={{ maxWidth: 420, width: "100%" }}>
      <Title order={1} ta="center">
        {heading}
      </Title>
      <Text ta="center" c="dimmed">
        {body}
      </Text>
      <form onSubmit={handleSubmit}>
        <Stack gap="sm">
          <PasswordInput
            label="Access token"
            placeholder="Enter the server access token"
            value={value}
            onChange={(e) => setValue(e.currentTarget.value)}
            required
          />
          <Button type="submit" w="100%" disabled={value === ""}>
            {heading === "Token rejected" ? "Save" : "Continue"}
          </Button>
        </Stack>
      </form>
    </Stack>
  );
}

function AppTokenGate({ onSubmit }: { onSubmit: (token: string) => void }) {
  return (
    <GateProvider>
      <TokenForm
        heading="OpenBooks"
        body="Enter the server access token to continue."
        onSubmit={onSubmit}
      />
    </GateProvider>
  );
}

function InvalidTokenGate({ onSubmit }: { onSubmit: (token: string) => void }) {
  return (
    <GateProvider>
      <TokenForm
        heading="Token rejected"
        body="The server rejected your token (401). Enter the current token:"
        onSubmit={onSubmit}
      />
    </GateProvider>
  );
}

function MainContent() {
  // Rendered below the MantineProvider so useStyles() resolves the real
  // theme and colour scheme for the full-viewport background.
  const { classes } = useStyles();
  return (
    <div className={classes.wrapper}>
      <SearchPage />
      <NotificationDrawer />
    </div>
  );
}

function AppInner() {
  const open = useAppSelector((state) => state.state.isSidebarOpen);

  return (
    <MantineProvider
      colorSchemeManager={{
        get: (defaultValue: MantineColorScheme) =>
          (localStorage.getItem("color-scheme") as MantineColorScheme) ||
          defaultValue,
        set: (value: MantineColorScheme) =>
          localStorage.setItem("color-scheme", value),
        subscribe: () => {},
        unsubscribe: () => {},
        clear: () => localStorage.removeItem("color-scheme")
      }}
      theme={{
        primaryColor: "brand",
        primaryShade: { light: 4, dark: 2 },
        colors: {
          brand: [
            "#e0ecff",
            "#b0c6ff",
            "#7e9fff",
            "#4c79ff",
            "#3366ff",
            "#0039e6",
            "#002db4",
            "#002082",
            "#001351",
            "#000621"
          ]
        },
        components: {
          ActionIcon: {
            defaultProps: {
              radius: "md",
              color: "brand"
            }
          }
        }
      }}
      stylesTransform={mantineStylesTransform}>
      <Notifications position="top-center" />
      <AppShell
        navbar={{
          width: { sm: 300 },
          breakpoint: "sm",
          collapsed: { desktop: !open, mobile: !open }
        }}
        padding={0}>
        <AppShell.Navbar>
          <Sidebar />
        </AppShell.Navbar>
        <AppShell.Main>
          <MainContent />
        </AppShell.Main>
      </AppShell>
    </MantineProvider>
  );
}

export default function App() {
  // The health probe is the gate decision: with a valid token the backend
  // answers 200 and the full UI renders. On a server running without auth
  // the endpoint still succeeds (noauth mode) and skips the gate too. A
  // 401 (or any failure) with a stored token goes to the rejection gate;
  // without a stored token the user is asked for one first.
  const { data: health } = useGetHealthQuery(null);
  const token = getToken();

  const submitToken = (t: string) => {
    setToken(t);
    window.location.reload();
  };

  if (health) {
    return <AppInner />;
  }
  if (!token) {
    return <AppTokenGate onSubmit={submitToken} />;
  }
  return <InvalidTokenGate onSubmit={submitToken} />;
}
