import { useEffect, useState } from "react";

export const usePageVisibility = () => {
  const getVisible = () =>
    typeof document === "undefined"
      ? true
      : document.visibilityState !== "hidden";

  const [visible, setVisible] = useState(getVisible);

  useEffect(() => {
    const onChange = () => setVisible(getVisible());

    document.addEventListener("visibilitychange", onChange);

    return () => {
      document.removeEventListener("visibilitychange", onChange);
    };
  }, []);

  return visible;
};
