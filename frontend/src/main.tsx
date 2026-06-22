import ReactDOM from "react-dom/client";
import App from "./App";
import "./lib/monaco"; // side-effect: configure Monaco workers, theme, loader
import "./styles/tokens.css";
import "./styles/global.css";
import "./styles/splash.css";

ReactDOM.createRoot(document.getElementById("root") as HTMLElement).render(<App />);
