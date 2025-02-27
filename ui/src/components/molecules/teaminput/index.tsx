import { useState, ChangeEvent } from 'react';

function TeamInput() {
  const [inputValue, setInputValue] = useState('');

  const handleInputChange = (event: ChangeEvent<HTMLInputElement>) => {
    setInputValue(event.target.value);
    localStorage.setItem("team_code", event.target.value);
  };

  const storedValue = localStorage.getItem("team_code");
  return (
    <div>
      <input
        type="text"
        value={inputValue}
        onChange={handleInputChange}
        placeholder="Paste team code here"
      />
      <p>Current Team: {storedValue}</p>
    </div>
  );
}

export default TeamInput;