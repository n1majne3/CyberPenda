import { useEffect } from "react";
import { useLocation, useNavigate } from "react-router-dom";

export function InAppNavigationButton({ to, label }: { to: string; label: string }) {
  const navigate = useNavigate();
  return <button type="button" onClick={() => navigate(to)}>{label}</button>;
}

export function LocationProbe({ onChange }: { onChange: (search: string) => void }) {
  const location = useLocation();
  useEffect(() => {
    onChange(location.search);
  }, [location.search, onChange]);
  return null;
}
